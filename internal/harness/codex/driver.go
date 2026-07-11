package codex

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// execCommand is the process-spawn seam driver tests replace; production
// uses exec.CommandContext. CI has no codex binary on PATH.
var execCommand = exec.CommandContext

// turnMu serializes turns per session: concurrent Injects into the same
// codex thread would interleave rollout writes.
//
// TODO: entries are never evicted (bounded by distinct TmuxSession/session-
// name cardinality, so not an unbounded leak) — tying eviction to process/
// agent teardown is future work.
var turnMu sync.Map // TmuxSession → *sync.Mutex

// aborts tracks the in-flight turn's cancel func per session so AbortTurn
// can kill it.
//
// TODO: same eviction caveat as turnMu above.
var aborts sync.Map // TmuxSession → context.CancelFunc

// TurnDriver drives codex turn-per-process: no resident process; each
// injected message spawns `codex exec … resume <thread-id> <msg>` in the
// workspace and blocks until it exits. Turns are serialized per session.
type TurnDriver struct{}

func (TurnDriver) Style() harness.DriveStyle { return harness.DriveTurns }

func lockFor(key string) *sync.Mutex {
	mu, _ := turnMu.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func (d TurnDriver) Start(ctx context.Context, h harness.SessionHandle) error {
	if h.OpeningPrompt == "" || h.IDs.Get() != "" {
		return nil // restart is bookkeeping; an existing thread just resumes on the next message
	}
	_, err := d.runTurn(ctx, h, h.OpeningPrompt)
	return err
}

func (d TurnDriver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	res, err := d.runTurn(ctx, h, msg)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// runTurn spawns one codex exec turn and blocks until it exits. A resume
// against a vanished thread ("no rollout found") clears the stored id and
// retries once as a fresh turn.
func (d TurnDriver) runTurn(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	mu := lockFor(h.TmuxSession)
	mu.Lock()
	defer mu.Unlock()

	run := func(resumeID string) (harness.Result, int, error) {
		turnCtx, cancel := context.WithCancel(ctx)
		aborts.Store(h.TmuxSession, cancel)
		defer func() { aborts.Delete(h.TmuxSession); cancel() }()

		args := append([]string{}, h.TurnArgs...)
		if resumeID != "" {
			args = append(args, "resume", resumeID)
		}
		args = append(args, msg)
		cmd := execCommand(turnCtx, Codex{}.Binary(), args...)
		cmd.Dir = h.Workspace
		cmd.Env = append(os.Environ(), envSlice(h.Env)...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
		runErr := cmd.Run()
		if runErr != nil && turnCtx.Err() != nil {
			// AbortTurn (or a cancelled parent ctx) killed the child mid-turn:
			// exec.CommandContext produces the same exit!=0 + empty-stdout
			// shape as a stale ("no rollout found") resume. Report the
			// cancellation explicitly so the caller never misclassifies an
			// abort as a stale thread — that would clear a perfectly valid
			// stored id and silently re-run the message as a fresh turn. A
			// cancelled ctx alone (without a non-nil runErr) must NOT
			// short-circuit a turn that actually completed successfully.
			return harness.Result{}, -1, fmt.Errorf("codex: turn cancelled: %w", turnCtx.Err())
		}
		res, perr := Codex{}.ParseEvents(&stdout)
		if perr != nil {
			return harness.Result{}, -1, perr
		}
		exit := 0
		if runErr != nil {
			// Any failure with empty output is the stale-thread shape —
			// EXCEPT a cancelled turn, which is handled above and never
			// reaches here.
			exit = 1
		}
		return res, exit, nil
	}

	id := h.IDs.Get()
	res, exit, err := run(id)
	if err != nil {
		return nil, err
	}
	if exit != 0 && id != "" && res.SessionID == "" && res.Text == "" {
		// Stale thread: clear and retry once fresh (one-step ladder).
		h.IDs.Clear()
		res, exit, err = run("")
		if err != nil {
			return nil, err
		}
	}
	if res.SessionID != "" {
		h.IDs.Set(res.SessionID)
	}
	if exit != 0 {
		res.IsError = true
	}
	appendTranscript(h, msg, res)
	return &res, nil
}

func (TurnDriver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	return harness.AttachSpec{HistoryPath: transcriptPath(h)}, nil
}

func (TurnDriver) AbortTurn(h harness.SessionHandle) error {
	if c, ok := aborts.Load(h.TmuxSession); ok {
		c.(context.CancelFunc)()
	}
	return nil
}

func transcriptPath(h harness.SessionHandle) string {
	return filepath.Join(h.HomePath, "state", "transcripts", h.TmuxSession+".log")
}

// appendTranscript records one turn (user message + result text) to the
// session's transcript file. Best-effort: a transcript write failure must
// never fail the turn itself.
func appendTranscript(h harness.SessionHandle, msg string, res harness.Result) {
	path := transcriptPath(h)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		log.Printf("codex: transcript for %s: mkdir %s: %v", h.TmuxSession, filepath.Dir(path), err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("codex: transcript for %s: open %s: %v", h.TmuxSession, path, err)
		return
	}
	defer f.Close()

	now := time.Now().Format(time.RFC3339)
	fmt.Fprintf(f, "--- %s user\n%s\n--- %s codex\n%s\n", now, msg, now, res.Text)
}

// envSlice renders an env map as "K=V" pairs for appending to exec.Cmd.Env.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
