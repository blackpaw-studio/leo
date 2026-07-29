package consult

import (
	"encoding/json"
	"io"
	"time"
)

// Status is a consult's lifecycle state. A consult is recorded as queued
// before it competes for a concurrency slot, so consults waiting behind the
// limit are visible too.
type Status string

const (
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusDone     Status = "done"
	StatusFailed   Status = "failed"
	StatusTimeout  Status = "timeout"
	StatusCanceled Status = "canceled"
)

// Terminal reports whether the consult has finished, however it ended.
func (s Status) Terminal() bool {
	switch s {
	case StatusDone, StatusFailed, StatusTimeout, StatusCanceled:
		return true
	}
	return false
}

// Record describes one consult. It is persisted next to the consult's event
// stream so `leo consult list` can report in-flight work without asking the
// daemon. The final answer is deliberately not duplicated here: it is the
// last event in the stream.
type Record struct {
	ID        string    `json:"id"`
	Caller    string    `json:"caller,omitempty"`
	Template  string    `json:"template"`
	Harness   string    `json:"harness"`
	Model     string    `json:"model"`
	Workspace string    `json:"workspace,omitempty"`
	Prompt    string    `json:"prompt"`
	Status    Status    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Elapsed reports how long the consult ran, or has been running so far.
func (r Record) Elapsed(now time.Time) time.Duration {
	if !r.EndedAt.IsZero() {
		return r.EndedAt.Sub(r.StartedAt)
	}
	return now.Sub(r.StartedAt)
}

// StaleAfter is how long past its own deadline a consult may sit
// non-terminal before it is presumed abandoned. Nothing updates a record
// once the daemon dies, and killing the daemon is routine — `leo update`
// and `leo service restart` both SIGKILL it — so without this a consult in
// flight at the wrong moment would stay "running" forever: a permanent
// phantom in `leo consult list`, a `leo consult watch` that never returns,
// and a retention budget that never reclaims the slot.
const StaleAfter = RunTimeout + 2*time.Minute

// Stale reports whether an unfinished consult has outlived any plausible
// run and should be treated as abandoned.
func (r Record) Stale(now time.Time) bool {
	return !r.Status.Terminal() && now.Sub(r.StartedAt) > StaleAfter
}

// Settled reports whether a consult has stopped changing, whether it
// finished or was abandoned. Callers waiting on a consult should stop here.
func (r Record) Settled(now time.Time) bool {
	return r.Status.Terminal() || r.Stale(now)
}

// streamEvent is one line of <id>.ndjson. Leo owns this framing rather than
// teeing the harness output verbatim because timestamps have to survive
// replay and no harness emits them. `jq .d` recovers the raw stream.
type streamEvent struct {
	// T is seconds since the consult started.
	T float64 `json:"t"`
	// D is the harness event verbatim, when the line was parseable JSON.
	D json.RawMessage `json:"d,omitempty"`
	// Raw carries a line that was not JSON — harness crash output and
	// stderr chatter, which otherwise vanishes into an error string.
	Raw string `json:"raw,omitempty"`
}

// Recorder persists consult records and their event streams.
type Recorder interface {
	// Open registers a consult and returns a handle whose Writer receives
	// the harness's raw output as it arrives.
	Open(Record) (Handle, error)
}

// Handle is one consult's open recording. Writes are best-effort: a
// recording failure is reported from Close rather than disturbing the
// consult itself.
type Handle interface {
	io.Writer
	SetStatus(Status) error
	Close(Status, error) error
}

// nopRecorder discards everything, so a Dispatcher built without a recorder
// behaves exactly as it did before recording existed.
type nopRecorder struct{}

func (nopRecorder) Open(Record) (Handle, error) { return nopHandle{}, nil }

type nopHandle struct{}

func (nopHandle) Write(p []byte) (int, error) { return len(p), nil }
func (nopHandle) SetStatus(Status) error      { return nil }
func (nopHandle) Close(Status, error) error   { return nil }
