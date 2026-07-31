package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// defaultSSEHeartbeat is the interval between SSE comment heartbeats on
// GET /api/v1/events, keeping idle proxies from closing the connection.
const defaultSSEHeartbeat = 20 * time.Second

// sseSubscriberBuffer is the bounded channel size requested from the event
// source. The bus (once it exists) is responsible for dropping a subscriber
// that can't keep up rather than blocking or growing this without bound.
const sseSubscriberBuffer = 32

// handleAPIState serves the whole observable world as one snapshot.
// GET /api/v1/state
func (s *Server) handleAPIState(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
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

	snap := buildSnapshot(snapshotInput{
		Config:        cfg,
		Records:       records,
		ProcessStates: processStates,
		CronEntries:   cronEntries,
		History:       s.loadHistory(cfg).All(),
		Activity:      s.activity,
		RunLog:        s.runLog,
		LeoVersion:    s.version,
		Now:           time.Now(),
	})

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: snap})
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
	// RunLog is the run log's read seam (observe.RunLog satisfies it). It
	// alone knows about in-flight runs, so it takes priority over History
	// for the runs it holds; History only tops up older completed runs the
	// (bounded, in-memory) run log has already evicted or never saw (e.g.
	// after a daemon restart). nil is a supported default — recent_runs is
	// then built from History alone, as before RunLog existed.
	RunLog     runProvider
	LeoVersion string
	Now        time.Time
}

// buildSnapshot assembles an observe.Snapshot from raw state. It is pure
// (inputs -> Snapshot) so it's unit-testable without an HTTP request or a
// running supervisor.
func buildSnapshot(in snapshotInput) observe.Snapshot {
	var activities map[string]observe.AgentActivity
	if in.Activity != nil {
		activities = in.Activity.Activities()
	}

	agents := make([]observe.Agent, 0, len(in.Records))
	for _, rec := range in.Records {
		agents = append(agents, buildAgent(rec, in.ProcessStates, activities, in.Config))
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })

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

	return observe.Snapshot{
		Version:    observe.SnapshotVersion,
		ServerTime: in.Now,
		LeoVersion: in.LeoVersion,
		Agents:     agents,
		Tasks:      tasks,
		RecentRuns: buildRecentRuns(in.History, liveRuns),
	}
}

// buildAgent maps one agent.Record to its observe.Agent view. Status,
// restarts, and started-at prefer the supervisor's in-memory process state
// (states) over the record — the agentstore-backed record can be stale for
// those fields — falling back to the record when the agent has no live
// process entry (e.g. a stopped worktree agent kept around for pruning).
func buildAgent(rec agent.Record, states map[string]ProcessStateInfo, activities map[string]observe.AgentActivity, cfg *config.Config) observe.Agent {
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
		Status:    mapStatus(rawStatus),
		Restarts:  restarts,
		StartedAt: startedAt,
		Activity:  observe.ActivityUnknown,
	}

	if cfg != nil && rec.Template != "" {
		if tmpl, ok := cfg.Templates[rec.Template]; ok {
			a.Model = cfg.TemplateModel(tmpl)
			a.Harness = cfg.TemplateHarness(tmpl)
		}
	}

	if act, ok := activities[rec.Name]; ok {
		a.Activity = act.Activity
		a.CurrentAction = act.CurrentAction
		if !act.LastActivityAt.IsZero() {
			t := act.LastActivityAt
			a.LastActivityAt = &t
		}
	}

	return a
}

// mapStatus maps a raw supervisor/record status string onto one of the four
// observe.Status constants. Anything unrecognized becomes observe.StatusStopped
// so the API never emits a status a consumer wasn't told to expect.
func mapStatus(raw string) observe.Status {
	switch observe.Status(raw) {
	case observe.StatusStarting, observe.StatusRunning, observe.StatusSuspended, observe.StatusStopped:
		return observe.Status(raw)
	default:
		return observe.StatusStopped
	}
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Long-lived response: disable the server's WriteTimeout for this
	// connection, mirroring internal/daemon/server.go's long-poll handler.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	w.WriteHeader(http.StatusOK)

	hello := observe.HelloPayload{Version: observe.SnapshotVersion, ServerTime: time.Now()}
	if err := writeSSEEvent(w, string(observe.EventHello), hello); err != nil {
		return
	}
	flusher.Flush()

	var events <-chan observe.Event
	if s.events != nil {
		var unsubscribe func()
		events, unsubscribe = s.events.Subscribe(sseSubscriberBuffer)
		defer unsubscribe()
	}

	heartbeat := s.sseHeartbeat
	if heartbeat <= 0 {
		heartbeat = defaultSSEHeartbeat
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			// events is nil when s.events is unset: a nil channel receive
			// never fires, so this case is simply inert (hello + heartbeats
			// only) rather than needing special-casing here.
			if !ok {
				// The source closed our subscription (e.g. we were dropped
				// as a slow consumer) — end the response cleanly so the
				// client reconnects rather than spinning on a closed channel.
				return
			}
			if err := writeSSEEvent(w, string(ev.Type), ev.Payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent writes one named SSE frame. payload is JSON-marshaled, never
// interpolated into markup — Action.Detail and similar fields carry untrusted
// display text, and json.Marshal is what keeps it safely escaped.
func writeSSEEvent(w io.Writer, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}
