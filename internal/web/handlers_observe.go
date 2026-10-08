package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/observe/httpapi"
)

// defaultSSEHeartbeat is the interval between SSE comment heartbeats on
// GET /api/v1/events, keeping idle proxies from closing the connection.
const defaultSSEHeartbeat = 20 * time.Second

// defaultSSEWriteTimeout bounds each individual write on GET /api/v1/events.
// A client that holds the connection open but stops reading (zero TCP
// window) would otherwise block the handler goroutine inside that write
// forever — r.Context() never fires because the connection itself never
// closes. Producers are unaffected: the bus already drops a subscriber once
// its buffer fills, so this bounds a goroutine leak only, not delivery.
const defaultSSEWriteTimeout = 30 * time.Second

// sseSubscriberBuffer is the bounded channel size requested from the event
// source. The bus (once it exists) is responsible for dropping a subscriber
// that can't keep up rather than blocking or growing this without bound.
const sseSubscriberBuffer = 32

// handleAPIState serves the whole observable world as one snapshot.
// GET /api/v1/state
func (s *Server) handleAPIState(w http.ResponseWriter, r *http.Request) {
	httpapi.ServeState(w, r, func(context.Context) (any, error) {
		// Read the event seq before any state: the snapshot then reflects at
		// least every event up to it, and a client applies only later ones.
		seq := s.eventSeq()
		cfg, err := s.loadConfig()
		if err != nil {
			return nil, err
		}

		var records []agent.Record
		if s.agentSvc != nil {
			records = s.agentSvc.List()
		}

		var processStates map[string]ProcessStateInfo
		if s.processes != nil {
			processStates = s.processes.States()
		}

		var cronEntries []cron.EntryInfo
		if s.scheduler != nil {
			cronEntries = s.scheduler.List()
		}

		return buildSnapshot(snapshotInput{
			Config:        cfg,
			Records:       records,
			ProcessStates: processStates,
			CronEntries:   cronEntries,
			History:       s.loadHistory(cfg).All(),
			Activity:      s.activity,
			Attention:     s.attention,
			SurfacedFiles: s.surfacedFiles,
			RunLog:        s.runLog,
			MessageLog:    s.messageLog,
			BridgeFeed:    s.bridgeFeed,
			Dispatches:    s.dispatches,
			LeoVersion:    s.version,
			Seq:           seq,
			Now:           time.Now(),
		}), nil
	}, func(w http.ResponseWriter, status int, data any, err error) {
		if err != nil {
			writeJSON(w, status, apiResponse{Error: err.Error()})
			return
		}
		writeJSON(w, status, apiResponse{OK: true, Data: data})
	})
}

// seqSource is an eventSource that also reports its last published seq
// (observe.Bus does).
type seqSource interface{ Seq() uint64 }

// eventSeq is the event stream's last seq, or 0 without a source that
// reports one.
func (s *Server) eventSeq() uint64 {
	if src, ok := s.events.(seqSource); ok {
		return src.Seq()
	}
	return 0
}

// snapshotInput is buildSnapshot's input: every raw source of world state,
// gathered by the handler so assembly itself stays a pure, directly
// unit-testable function. Zero-valued fields degrade gracefully (e.g. a nil
// Config skips model/harness resolution and task listing).
type snapshotInput struct {
	Config        *config.Config
	Records       []agent.Record
	ProcessStates map[string]ProcessStateInfo
	CronEntries   []cron.EntryInfo
	History       map[string][]history.Entry
	Activity      observe.ActivityProvider
	// Attention is the attention store's read seam. nil leaves every
	// agent's attention absent.
	Attention attentionProvider
	// SurfacedFiles is the surfaced-file store's read seam. nil leaves
	// every agent's surfaced_files absent.
	SurfacedFiles surfacedProvider
	// RunLog is the run log's read seam (observe.RunLog satisfies it). It
	// alone knows about in-flight runs, so it takes priority over History
	// for the runs it holds; History only tops up older completed runs the
	// (bounded, in-memory) run log has already evicted or never saw (e.g.
	// after a daemon restart). nil is a supported default — recent_runs is
	// then built from History alone, as before RunLog existed.
	RunLog runProvider
	// MessageLog is the agent-message log's read seam (observe.MessageLog
	// satisfies it). Optional: nil yields an empty recent_messages.
	MessageLog messageProvider
	// BridgeFeed is the bridge feed's read seam. Optional: nil leaves usage,
	// bridge, and attention reasons absent. Merged per agent by buildAgent.
	BridgeFeed bridgeFeedProvider
	// Dispatches is the dispatch store's read seam. Optional: nil leaves
	// dispatches and outstanding dispatch counts absent.
	Dispatches dispatchProvider
	LeoVersion string
	// Seq is the event stream's last seq when the snapshot began (see
	// observe.Snapshot.Meta).
	Seq uint64
	Now time.Time
}

// bridgeFeedProvider is the narrow read seam onto observe.BridgeFeed.
type bridgeFeedProvider interface {
	// BridgeAgents returns each bridged agent's projected state, keyed by agent name.
	BridgeAgents() map[string]observe.BridgeAgentState
}

// dispatchProvider is the narrow read seam onto the dispatch store's
// observability projection.
type dispatchProvider interface {
	observe.DispatchCounter
	// Dispatches returns live dispatches plus those ended within observe.DispatchLinger of now.
	Dispatches(now time.Time) []observe.Dispatch
}

// buildSnapshot assembles an observe.Snapshot from raw state. It is pure
// (inputs -> Snapshot) so it's unit-testable without an HTTP request or a
// running supervisor.
func buildSnapshot(in snapshotInput) observe.Snapshot {
	agents := projectAgents(in.Records, in.ProcessStates, AgentSources{
		Activity:   in.Activity,
		Attention:  in.Attention,
		Surfaced:   in.SurfacedFiles,
		BridgeFeed: in.BridgeFeed,
		Dispatches: in.Dispatches,
	}, in.Config)

	nextRun := make(map[string]time.Time, len(in.CronEntries))
	for _, e := range in.CronEntries {
		nextRun[e.Name] = e.Next
	}

	var tasks []observe.Task
	if in.Config != nil {
		tasks = make([]observe.Task, 0, len(in.Config.Tasks))
		for name, t := range in.Config.Tasks {
			tasks = append(tasks, buildTask(name, t, in.Config, nextRun[name], lastRunAt(in.History[name])))
		}
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	}

	var liveRuns []observe.TaskRun
	if in.RunLog != nil {
		liveRuns = in.RunLog.Recent(observe.MaxRecentRuns)
	}

	// Empty, never null: consumers range over this without a nil check.
	recentMessages := []observe.AgentMessage{}
	if in.MessageLog != nil {
		recentMessages = in.MessageLog.Recent(observe.MaxRecentMessages, in.Now)
	}

	return observe.Snapshot{
		Meta:           observe.SnapshotMeta{Seq: in.Seq},
		Version:        observe.SnapshotVersion,
		ServerTime:     in.Now,
		LeoVersion:     in.LeoVersion,
		Agents:         agents,
		Tasks:          tasks,
		RecentRuns:     buildRecentRuns(in.History, liveRuns),
		RecentMessages: recentMessages,
		Dispatches:     dispatchSnapshot(in.Dispatches, in.Now),
	}
}

// dispatchSnapshot reads the dispatch list once, nil-safe.
func dispatchSnapshot(p dispatchProvider, now time.Time) []observe.Dispatch {
	if p == nil {
		return []observe.Dispatch{}
	}
	// Empty, never null: consumers range over this without a nil check.
	return append([]observe.Dispatch{}, p.Dispatches(now)...)
}

// DispatchRows is the snapshot's dispatches (live plus lingering) for an
// agent-source dispatch seam; the same set GET /api/v1/state reports. A
// source that only counts dispatches yields an empty list.
func DispatchRows(src observe.DispatchCounter, now time.Time) []observe.Dispatch {
	p, _ := src.(dispatchProvider)
	return dispatchSnapshot(p, now)
}

// agentViews is every per-agent source read once for one projection.
type agentViews struct {
	activities  map[string]observe.AgentActivity
	attention   map[string]observe.AgentAttention
	surfaced    map[string][]observe.SurfacedFile
	bridge      map[string]observe.BridgeAgentState
	dispatches  map[string]int
	bridgeWired bool
}

func readAgentViews(src AgentSources) agentViews {
	v := agentViews{
		attention:   attentionSnapshot(src.Attention),
		surfaced:    surfacedSnapshot(src.Surfaced),
		bridgeWired: src.BridgeFeed != nil,
	}
	if src.Activity != nil {
		v.activities = src.Activity.Activities()
	}
	if src.BridgeFeed != nil {
		v.bridge = src.BridgeFeed.BridgeAgents()
	}
	if src.Dispatches != nil {
		v.dispatches = src.Dispatches.OutstandingDispatches()
	}
	return v
}

// buildAgent maps one agent.Record to its observe.Agent view. Status,
// restarts, and started-at prefer the supervisor's in-memory process state
// (states) over the record — the agentstore-backed record can be stale for
// those fields — falling back to the record when the agent has no live
// process entry (e.g. a stopped worktree agent kept around for pruning).
func buildAgent(rec agent.Record, states map[string]ProcessStateInfo, views agentViews, cfg *config.Config) observe.Agent {
	rawStatus := rec.Status
	restarts := rec.Restarts
	startedAt := rec.StartedAt
	if st, ok := states[rec.Name]; ok {
		rawStatus = st.Status
		restarts = st.Restarts
		if !st.StartedAt.IsZero() {
			startedAt = st.StartedAt
		}
	}

	a := observe.Agent{
		Name:      rec.Name,
		Template:  rec.Template,
		Repo:      rec.Repo,
		Workspace: rec.Workspace,
		Branch:    rec.Branch,
		Restarts:  restarts,
		StartedAt: startedAt,
		Activity:  observe.ActivityUnknown,
	}
	a.Status, a.WakeOnMessage = observe.AgentDormancy(rawStatus, rec.WakeOnMessage)

	if cfg != nil && rec.Template != "" {
		if tmpl, ok := cfg.Templates[rec.Template]; ok {
			a.Model = cfg.TemplateModel(tmpl)
			a.Harness = cfg.TemplateHarness(tmpl)
		}
	}

	if act, ok := views.activities[rec.Name]; ok {
		a.Activity = act.Activity
		a.CurrentAction = act.CurrentAction
		if !act.LastActivityAt.IsZero() {
			t := act.LastActivityAt
			a.LastActivityAt = &t
		}
	}

	if att, ok := views.attention[rec.Name]; ok {
		a.Attention = &att
	}
	a.SurfacedFiles = views.surfaced[rec.Name]
	mergeBridge(&a, views)

	return a
}

// mergeBridge folds the bridge feed's reading and the outstanding child
// counts into a. A claude agent the feed has no reading for reads as
// bridge absent; other harnesses carry no bridge field.
func mergeBridge(a *observe.Agent, views agentViews) {
	bs, ok := views.bridge[a.Name]
	switch {
	case ok:
		a.Bridge = bs.Bridge
		a.Usage = bs.Usage
		// A connected bridge is authoritative for the running tool, nil
		// included: the pane reading may lag it. Otherwise the bridge
		// fills in only what it knows.
		if bs.Bridge == observe.BridgeConnected || bs.CurrentAction != nil {
			a.CurrentAction = bs.CurrentAction
		}
		if a.Attention != nil && a.Attention.Reason == nil && a.Attention.State == observe.AttentionNeedsInput && bs.Reason != nil {
			reason := *bs.Reason
			a.Attention.Reason = &reason
		}
	case views.bridgeWired && a.Harness == "claude":
		a.Bridge = observe.BridgeAbsent
	}
	if o := (observe.Outstanding{Dispatches: views.dispatches[a.Name], Subagents: bs.Subagents}); o.Dispatches > 0 || o.Subagents > 0 {
		a.Outstanding = &o
	}
}

// attentionProvider is the narrow read seam onto observe.AttentionStore.
type attentionProvider interface {
	All() map[string]observe.AgentAttention
}

// attentionSnapshot reads every agent's attention once, nil-safe.
func attentionSnapshot(p attentionProvider) map[string]observe.AgentAttention {
	if p == nil {
		return nil
	}
	return p.All()
}

// surfacedProvider is the narrow read seam onto observe.SurfacedFileStore.
type surfacedProvider interface {
	All() map[string][]observe.SurfacedFile
}

// surfacedSnapshot reads every agent's surfaced files once, nil-safe. A
// typed-nil store is nil-safe itself, so only the interface needs checking.
func surfacedSnapshot(p surfacedProvider) map[string][]observe.SurfacedFile {
	if p == nil {
		return nil
	}
	return p.All()
}

// AgentSources are the per-agent read seams an agent projection merges.
// Every field is optional; a nil one leaves its fields absent.
type AgentSources struct {
	Activity   observe.ActivityProvider
	Attention  attentionProvider
	Surfaced   surfacedProvider
	BridgeFeed bridgeFeedProvider
	Dispatches observe.DispatchCounter
}

// ProjectAgents builds the same rows exposed by /api/v1/state.data.agents.
func ProjectAgents(records []agent.Record, states map[string]ProcessStateInfo, src AgentSources, cfg *config.Config) []observe.Agent {
	return projectAgents(records, states, src, cfg)
}

func projectAgents(records []agent.Record, states map[string]ProcessStateInfo, src AgentSources, cfg *config.Config) []observe.Agent {
	views := readAgentViews(src)
	out := make([]observe.Agent, 0, len(records))
	for _, rec := range records {
		out = append(out, buildAgent(rec, states, views, cfg))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildTask maps one configured task to its observe.Task view.
func buildTask(name string, t config.TaskConfig, cfg *config.Config, next time.Time, last time.Time) observe.Task {
	runtime := t.Runtime
	if runtime == "" {
		runtime = "oneshot"
	}
	task := observe.Task{
		Name:      name,
		Schedule:  t.Schedule,
		Timezone:  t.Timezone,
		Enabled:   t.Enabled,
		Runtime:   runtime,
		Template:  t.Template,
		Workspace: cfg.TaskWorkspace(t),
		Model:     cfg.TaskModel(t),
		Harness:   cfg.TaskHarness(t),
	}
	if !last.IsZero() {
		lastCopy := last
		task.LastRunAt = &lastCopy
	}
	if !next.IsZero() {
		nextCopy := next
		task.NextRunAt = &nextCopy
	}
	return task
}

// lastRunAt returns the most recent run time for a task's history entries.
// Record prepends new entries, so the newest is always index 0.
func lastRunAt(entries []history.Entry) time.Time {
	if len(entries) == 0 {
		return time.Time{}
	}
	return entries[0].RunAt
}

// buildRecentRuns merges the run log's live view (live, newest first —
// the only source that knows about a currently-running firing) with
// history-derived runs, newest first overall, deduplicated by ID, and capped
// at observe.MaxRecentRuns. The run log wins on a duplicate ID: it carries
// honest in-process timing, whereas a history entry can only ever describe a
// firing that already finished.
func buildRecentRuns(hist map[string][]history.Entry, live []observe.TaskRun) []observe.TaskRun {
	runs := make([]observe.TaskRun, len(live))
	copy(runs, live)

	seen := make(map[string]bool, len(live))
	for _, r := range live {
		seen[r.ID] = true
	}

	for _, entries := range hist {
		for _, e := range entries {
			run := historyEntryToRun(e)
			if seen[run.ID] {
				continue
			}
			seen[run.ID] = true
			runs = append(runs, run)
		}
	}

	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	if len(runs) > observe.MaxRecentRuns {
		runs = runs[:observe.MaxRecentRuns]
	}
	return runs
}

// historyEntryToRun converts one history.Entry to an observe.TaskRun.
//
// When the entry carries StartedAt (recorded by internal/run since
// RecordTimed), EndedAt/DurationMS are derived honestly from it. Legacy
// entries recorded before those fields existed have no StartedAt — RunAt
// (the only timestamp they carry, stamped at completion) becomes the
// best-effort StartedAt since TaskRun.StartedAt is mandatory, but EndedAt and
// DurationMS are left nil rather than fabricating started_at == ended_at.
func historyEntryToRun(e history.Entry) observe.TaskRun {
	run := observe.TaskRun{
		ID:     historyRunID(e),
		Task:   e.Task,
		Status: runStatus(e),
		Error:  runError(e),
	}

	if e.StartedAt.IsZero() {
		run.StartedAt = e.RunAt
		return run
	}

	run.StartedAt = e.StartedAt
	ended := e.RunAt
	run.EndedAt = &ended

	durationMS := e.DurationMS
	if durationMS == 0 {
		durationMS = ended.Sub(e.StartedAt).Milliseconds()
	}
	run.DurationMS = &durationMS
	return run
}

// historyRunID derives the same ID format internal/run's producers use
// (taskName + "-" + startTime.UnixNano()) whenever a StartedAt is known, so a
// history-derived run correctly dedupes against the run log's copy of the
// same firing. Legacy entries with no StartedAt fall back to RunAt — they
// can never collide with a live run log entry anyway, since the run log is
// wiped on every daemon restart and legacy entries by definition predate this
// field.
func historyRunID(e history.Entry) string {
	t := e.StartedAt
	if t.IsZero() {
		t = e.RunAt
	}
	return fmt.Sprintf("%s-%d", e.Task, t.UnixNano())
}

func runStatus(e history.Entry) observe.RunStatus {
	if e.ExitCode == 0 && (e.Reason == "" || e.Reason == history.ReasonSuccess) {
		return observe.RunSucceeded
	}
	return observe.RunFailed
}

func runError(e history.Entry) string {
	if runStatus(e) == observe.RunSucceeded {
		return ""
	}
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("exit code %d", e.ExitCode)
}

// handleAPIEvents streams SSE frames: a hello event on connect, then any
// events published on the wired event source, plus a periodic comment
// heartbeat. No envelope (unlike every other /api route) — SSE frames are
// written directly, per the wire contract.
// GET /api/v1/events
func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	httpapi.ServeEvents(w, r, httpapi.EventsOptions{
		Source: s.events, Heartbeat: s.sseHeartbeat, WriteTimeout: s.sseWriteTimeout, Buffer: sseSubscriberBuffer,
	})
}
