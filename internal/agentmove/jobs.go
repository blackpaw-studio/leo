package agentmove

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Job states.
const (
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// maxFinishedJobs bounds how many finished jobs the registry keeps for
// late pollers; the oldest are dropped first.
const maxFinishedJobs = 64

// StageEvent is one stage a job reached.
type StageEvent struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

// JobStatus is a job as pollers see it.
type JobStatus struct {
	ID      string       `json:"id"`
	Request Request      `json:"request"`
	State   string       `json:"state"`
	Stages  []StageEvent `json:"stages"`
	Notes   []Problem    `json:"notes,omitempty"`
	// Error is the failure's code and full message; Problems lists every
	// destination refusal when the preflight said no.
	Error      *Problem   `json:"error,omitempty"`
	Problems   []Problem  `json:"problems,omitempty"`
	Result     *Result    `json:"result,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// job is one running or finished transfer.
type job struct {
	mu    sync.Mutex
	st    JobStatus
	clock Clock
	done  chan struct{}
}

func (j *job) Stage(name string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.st.Stages = append(j.st.Stages, StageEvent{Name: name, At: j.clock.Now()})
}

func (j *job) Note(p Problem) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.st.Notes = append(j.st.Notes, p)
}

func (j *job) finish(res Result, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.clock.Now()
	j.st.FinishedAt = &now
	if err == nil {
		j.st.State, j.st.Result = JobDone, &res
	} else {
		j.st.State = JobFailed
		j.st.Error = &Problem{Code: CodeOf(err), Message: err.Error()}
		var ps Problems
		if errors.As(err, &ps) {
			j.st.Problems = ps
		}
	}
	close(j.done)
}

func (j *job) status() JobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.st
	st.Stages = append([]StageEvent(nil), j.st.Stages...)
	st.Notes = append([]Problem(nil), j.st.Notes...)
	return st
}

// Jobs runs transfers in the background, one per agent at a time, and keeps
// their status for polling: a CLI, web or MCP caller gets an id at once,
// and a move of the calling agent outlives that agent's own session.
type Jobs struct {
	mover *Mover
	mu    sync.Mutex
	jobs  map[string]*job
	order []string
	// active maps an agent to its running job.
	active map[string]string
}

// NewJobs returns an empty registry over mover.
func NewJobs(mover *Mover) *Jobs {
	return &Jobs{mover: mover, jobs: map[string]*job{}, active: map[string]string{}}
}

// Start begins req and returns its first status. ctx only scopes the
// call: the job itself outlives the request that started it.
func (js *Jobs) Start(ctx context.Context, req Request) (JobStatus, error) {
	if req.Agent == "" {
		return JobStatus{}, problemf(CodeBadRequest, "no agent named")
	}
	id, err := newJobID()
	if err != nil {
		return JobStatus{}, err
	}
	js.mu.Lock()
	if running, ok := js.active[req.Agent]; ok {
		js.mu.Unlock()
		return JobStatus{}, problemf(CodeAgentMoving, "%s is already being transferred (job %s)", req.Agent, running)
	}
	clock := js.mover.Clock
	j := &job{clock: clock, done: make(chan struct{}),
		st: JobStatus{ID: id, Request: req, State: JobRunning, StartedAt: clock.Now()}}
	js.jobs[id] = j
	js.order = append(js.order, id)
	js.active[req.Agent] = id
	js.prune()
	js.mu.Unlock()

	go func() {
		res, err := js.mover.Run(context.WithoutCancel(ctx), req, j)
		j.finish(res, err)
		js.mu.Lock()
		delete(js.active, req.Agent)
		js.mu.Unlock()
	}()
	return j.status(), nil
}

// prune drops the oldest finished jobs beyond maxFinishedJobs; js.mu held.
func (js *Jobs) prune() {
	finished := 0
	for _, id := range js.order {
		if js.jobs[id].status().State != JobRunning {
			finished++
		}
	}
	kept := js.order[:0]
	for _, id := range js.order {
		if finished > maxFinishedJobs && js.jobs[id].status().State != JobRunning {
			delete(js.jobs, id)
			finished--
			continue
		}
		kept = append(kept, id)
	}
	js.order = kept
}

// Get returns job id's status.
func (js *Jobs) Get(id string) (JobStatus, bool) {
	js.mu.Lock()
	j, ok := js.jobs[id]
	js.mu.Unlock()
	if !ok {
		return JobStatus{}, false
	}
	return j.status(), true
}

// Wait blocks until job id finishes or ctx ends.
func (js *Jobs) Wait(ctx context.Context, id string) (JobStatus, error) {
	js.mu.Lock()
	j, ok := js.jobs[id]
	js.mu.Unlock()
	if !ok {
		return JobStatus{}, problemf(CodeBadRequest, "no move job %q", id)
	}
	select {
	case <-j.done:
		return j.status(), nil
	case <-ctx.Done():
		return j.status(), ctx.Err()
	}
}

func newJobID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "mv-" + hex.EncodeToString(b), nil
}
