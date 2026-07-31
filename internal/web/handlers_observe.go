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
	LeoVersion    string
	Now           time.Time
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

	return observe.Snapshot{
		Version:    observe.SnapshotVersion,
		ServerTime: in.Now,
		LeoVersion: in.LeoVersion,
		Agents:     agents,
		Tasks:      tasks,
		RecentRuns: buildRecentRuns(in.History),
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

// buildRecentRuns flattens every task's history into observe.TaskRun,
// newest first, capped at observe.MaxRecentRuns.
//
// history.Entry only records a single timestamp for a completed run (no
// separate start time or duration), so StartedAt and EndedAt are both set to
// it and DurationMS is left unset — the best fidelity the existing history
// store can offer without touching internal/run.
func buildRecentRuns(hist map[string][]history.Entry) []observe.TaskRun {
	var runs []observe.TaskRun
	for _, entries := range hist {
		for _, e := range entries {
			ended := e.RunAt
			runs = append(runs, observe.TaskRun{
				ID:        fmt.Sprintf("%s-%d", e.Task, e.RunAt.UnixNano()),
				Task:      e.Task,
				Status:    runStatus(e),
				StartedAt: e.RunAt,
				EndedAt:   &ended,
				Error:     runError(e),
			})
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	if len(runs) > observe.MaxRecentRuns {
		runs = runs[:observe.MaxRecentRuns]
	}
	return runs
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
