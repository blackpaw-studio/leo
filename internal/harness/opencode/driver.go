package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// execCommand is the process-spawn seam driver tests replace; production
// uses exec.CommandContext. CI has no opencode binary on PATH.
var execCommand = exec.CommandContext

// turnMu serializes turns per session: concurrent Injects into the same
// attach connection would interleave server-side turn state.
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

// healthPollInterval/healthPollBudget govern Start's readiness probe against
// `opencode serve`'s /global/health endpoint. Vars (not consts) so tests can
// shrink the budget instead of waiting out the real one.
var (
	healthPollInterval = 500 * time.Millisecond
	healthPollBudget   = 60 * time.Second
)

// ServerDriver drives opencode's headless server: the supervised resident
// process is `opencode serve`; messages go in via `opencode run --attach`
// (which blocks until the turn completes — attach-mode event forwarding is
// lossy, so process exit, not step_finish, is the turn-end signal); attach
// is opencode's own TUI client.
type ServerDriver struct{}

func (ServerDriver) Style() harness.DriveStyle { return harness.DriveTmux }

func lockFor(key string) *sync.Mutex {
	mu, _ := turnMu.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// Start waits for `opencode serve` to answer healthy on its provisioned
// port, then — if this session has never sent a first message — delivers
// the opening prompt via one Inject in the background. The IDs-empty guard
// makes restarts prompt-safe: a serve crash and restart never re-sends the
// opening prompt because the stored session id survives the crash
// (RecoverQuickExit keeps it).
func (d ServerDriver) Start(ctx context.Context, h harness.SessionHandle) error {
	state, err := LoadServerState(h.HomePath, h.TmuxSession)
	if err != nil {
		return fmt.Errorf("opencode: loading server state: %w", err)
	}
	if err := waitForHealth(ctx, state.URL()); err != nil {
		return err
	}
	if h.OpeningPrompt != "" && h.IDs.Get() == "" {
		go func() {
			if _, err := d.Inject(ctx, h, h.OpeningPrompt); err != nil {
				log.Printf("opencode: opening prompt for %s: %v", h.TmuxSession, err)
			}
		}()
	}
	return nil
}

// waitForHealth polls GET <baseURL>/global/health until it reports
// {"healthy":true}, the budget elapses, or ctx is cancelled.
func waitForHealth(ctx context.Context, baseURL string) error {
	url := baseURL + "/global/health"
	deadline := time.Now().Add(healthPollBudget)
	for {
		if isHealthy(ctx, url) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("opencode: server at %s did not become healthy within %s", baseURL, healthPollBudget)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthPollInterval):
		}
	}
}

func isHealthy(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Healthy bool `json:"healthy"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return false
	}
	return body.Healthy
}

// Inject delivers one message via `opencode run --attach`, which blocks
// until the turn completes server-side. Turns are serialized per session.
func (d ServerDriver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	mu := lockFor(h.TmuxSession)
	mu.Lock()
	defer mu.Unlock()

	state, err := LoadServerState(h.HomePath, h.TmuxSession)
	if err != nil {
		return nil, fmt.Errorf("opencode: loading server state: %w", err)
	}

	run := func(sessionID string) (harness.Result, int, error) {
		turnCtx, cancel := context.WithCancel(ctx)
		aborts.Store(h.TmuxSession, cancel)
		defer func() { aborts.Delete(h.TmuxSession); cancel() }()

		args := []string{"run", "--attach", state.URL(), "--format", "json", "--dir", h.Workspace}
		if state.Model != "" {
			args = append(args, "--model", state.Model)
		}
		if sessionID != "" {
			args = append(args, "-s", sessionID)
		}
		args = append(args, msg)
		cmd := execCommand(turnCtx, Opencode{}.Binary(), args...)
		cmd.Dir = h.Workspace
		cmd.Env = append(os.Environ(), "OPENCODE_SERVER_PASSWORD="+state.Password)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = io.Discard // stderr carries ANSI-coded errors; exit code + empty stdout is the signal
		runErr := cmd.Run()
		if runErr != nil && turnCtx.Err() != nil {
			// AbortTurn (or a cancelled parent ctx) killed the child mid-turn:
			// exec.CommandContext produces the same exit!=0 + empty-stdout
			// shape as a stale ("Session not found") -s id. Report the
			// cancellation explicitly so the caller never misclassifies an
			// abort as a stale session — that would clear a perfectly valid
			// stored id and silently re-run the message as a fresh turn. A
			// cancelled ctx alone (without a non-nil runErr) must NOT
			// short-circuit a turn that actually completed successfully.
			return harness.Result{}, -1, fmt.Errorf("opencode: turn cancelled: %w", turnCtx.Err())
		}
		res, perr := Opencode{}.ParseEvents(&stdout)
		if perr != nil {
			return harness.Result{}, -1, perr
		}
		exit := 0
		if runErr != nil {
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
		// Any non-cancelled failure with empty output is the stale-session
		// ("Session not found") shape — a cancelled turn is handled above
		// and never reaches here.
		h.IDs.Clear() // "Session not found" shape: retry once fresh
		res, exit, err = run("")
		if err != nil {
			return nil, err
		}
	}
	if res.SessionID == "" && exit == 0 {
		res.SessionID = latestSessionIDForDir(ctx, h.Workspace) // `opencode session list --format json` fallback (lossy attach stream)
	}
	if res.SessionID != "" {
		h.IDs.Set(res.SessionID)
	}
	if exit != 0 {
		res.IsError = true
	}
	return &res, nil
}

// latestSessionIDForDir runs `opencode session list --format json`, filters
// entries to workspace, and returns the newest one's id — or "" on any
// failure (a missing id is tolerable; the next turn's fallback retries).
func latestSessionIDForDir(ctx context.Context, workspace string) string {
	cmd := execCommand(ctx, Opencode{}.Binary(), "session", "list", "--format", "json")
	cmd.Dir = workspace
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return ""
	}
	var entries []struct {
		ID        string `json:"id"`
		Created   int64  `json:"created"`
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		return ""
	}
	var bestID string
	var bestCreated int64 = -1
	for _, e := range entries {
		if !samePath(e.Directory, workspace) {
			continue
		}
		if e.Created > bestCreated {
			bestCreated = e.Created
			bestID = e.ID
		}
	}
	return bestID
}

// samePath reports whether a and b refer to the same filesystem location.
// A plain string/Clean comparison is insufficient: opencode reports its own
// os.Getwd()-derived path, and on macOS /tmp is a symlink to /private/tmp —
// a leo workspace configured as /tmp/... would otherwise never match
// opencode's self-reported /private/tmp/... Falls back to the Clean
// comparison if EvalSymlinks fails on either side (e.g. the dir no longer
// exists) rather than erroring, since a missing match here is tolerable —
// the caller's fallback just returns "".
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, aErr := filepath.EvalSymlinks(a)
	rb, bErr := filepath.EvalSymlinks(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return ra == rb
}

// Attach returns the argv for opencode's own TUI client, pointed at the
// same server. The password rides in argv here (not env) because attach is
// interactive/user-invoked and, over ssh, env is not forwarded.
func (ServerDriver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	state, err := LoadServerState(h.HomePath, h.TmuxSession)
	if err != nil {
		return harness.AttachSpec{}, fmt.Errorf("opencode: loading server state: %w", err)
	}
	argv := []string{"opencode", "attach", state.URL(), "--dir", h.Workspace, "-p", state.Password}
	if id := h.IDs.Get(); id != "" {
		argv = append(argv, "-s", id)
	}
	return harness.AttachSpec{Argv: argv}, nil
}

// RecoverQuickExit: a crashed `opencode serve` is not a poisoned
// conversation — the stored session id must survive restarts, and the same
// serve args (same provisioned port) are reused unchanged.
func (ServerDriver) RecoverQuickExit(args []string) ([]string, harness.QuickExitAction) {
	return args, harness.QuickExitNone
}

func (ServerDriver) AbortTurn(h harness.SessionHandle) error {
	if c, ok := aborts.Load(h.TmuxSession); ok {
		c.(context.CancelFunc)()
	}
	return nil
}
