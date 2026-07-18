package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/web"
)

var execCommand = exec.Command

// lookPathFn is exec.LookPath, indirected through a package var so tests can
// stub the per-harness binary-on-PATH prereq check without requiring the
// real binary to be installed.
var lookPathFn = exec.LookPath

const silentPreamble = `SILENT SCHEDULED RUN — You are running as a scheduled background task, not responding to a user message.
Work silently. Do not narrate your process or describe your tool usage.
When finished:
- If there is something the user needs to see, deliver ONLY the final user-facing message via a configured channel plugin (see $LEO_CHANNELS).
- If there is nothing to report, or no channel plugin is configured, output exactly: NO_REPLY
Do not include status updates, tool output, or process descriptions.
`

// notifyFailureTimeout bounds the notify-on-fail child invocation so a
// failing task doesn't cascade into an unbounded second run.
const notifyFailureTimeout = 60 * time.Second

// maxChannelInitAttempts caps the number of extra attempts granted when a
// configured channel plugin's MCP server fails to initialize. These retries
// are infra-flake recovery, not part of the task's own retry budget.
const maxChannelInitAttempts = 2

// channelInitBackoff is the pause between channel-init-failure retries. A
// package-level var (not const) so tests can shrink it to speed up the
// retry-exhaustion path.
var channelInitBackoff = 2 * time.Second

// interruptResendInterval is how often a forwarded SIGINT/SIGTERM is re-sent
// to the child's process group while waiting for the child to exit.
const interruptResendInterval = 20 * time.Millisecond

// interruptGracePeriod bounds how long a forwarded interrupt is given to bring
// the child down on its own terms before executeCommand escalates to SIGKILL,
// so an interrupt can never leave the caller blocked indefinitely. A
// package-level var (not const) so tests can shrink it.
var interruptGracePeriod = 2 * time.Second

// notifyOutputSeparator prefixes the notify-on-fail child's captured output
// when it is appended to the task's log file.
const notifyOutputSeparator = "\n--- notify-on-fail output ---\n"

// errChannelMCPInit signals that a configured channel plugin's MCP server
// failed to initialize during a claude invocation (reported in the
// stream-json system/init event). The runner treats this as a distinguishable,
// retryable infra flake rather than a genuine task failure.
var errChannelMCPInit = errors.New("channel plugin MCP failed to initialize")

// errInterrupted signals that executeCommand's forwarder delivered a
// SIGINT/SIGTERM the parent itself received (e.g. Ctrl-C on `leo run`) to the
// child, rather than the child failing, timing out, or a channel MCP server
// failing to init. Run treats this as "the user asked us to stop" — it must
// not be retried, must not clear the task's session, and must not fire a
// notify-on-fail child.
var errInterrupted = errors.New("interrupted by signal")

// signalNotifyFn is signal.Notify, indirected through a package var so tests
// can inject a fake that captures the channel executeCommand registers and
// push a synthetic signal into it — without ever signaling the test process
// itself (which would risk killing `go test`).
var signalNotifyFn = signal.Notify

// resolveTask looks up a task by name.
func resolveTask(cfg *config.Config, taskName string) (config.TaskConfig, error) {
	if task, ok := cfg.Tasks[taskName]; ok {
		return task, nil
	}
	return config.TaskConfig{}, fmt.Errorf("task %q not found in config", taskName)
}

// Preview returns the assembled prompt and CLI args without executing.
func Preview(cfg *config.Config, taskName string, sessions *session.Store) (string, []string, error) {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return "", nil, err
	}

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return "", nil, fmt.Errorf("assembling prompt: %w", err)
	}

	var sessionID string
	if sessions != nil {
		sid, _, getErr := sessions.Get("task:" + taskName)
		if getErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read session store: %v\n", getErr)
		}
		sessionID = sid
	}

	leoEnv := leoMCPEnv(cfg, taskName)

	args, _ := buildArgs(cfg, task, taskName, prompt, sessionID, leoEnv)
	return prompt, args, nil
}

// Run executes a scheduled task.
func Run(cfg *config.Config, taskName string, sessions *session.Store) error {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return err
	}

	// Persistent tasks are dispatched through the daemon's session router
	// instead of spawning a fresh claude process. The seam keeps the legacy
	// one-shot path completely unaffected.
	if task.Runtime == "persistent" {
		return persistentImpl(cfg, taskName)
	}

	h, err := harness.Get(cfg.TaskHarness(task))
	if err != nil {
		return err
	}
	// Per-harness prereq: fail fast, before acquiring the task lock or doing
	// any other work, if the harness binary isn't on PATH. Preview
	// deliberately skips this check — previews must work without the binary
	// installed.
	if _, err := lookPathFn(h.Binary()); err != nil {
		return fmt.Errorf("task %q uses the %s harness, but %q was not found in PATH — install it or change the task's harness", taskName, h.Name(), h.Binary())
	}

	// Acquire task lock to prevent concurrent execution
	lockPath := filepath.Join(cfg.StatePath(), taskName+".lock")
	if err := acquireTaskLock(lockPath); err != nil {
		return fmt.Errorf("task %q is already running: %w", taskName, err)
	}
	defer releaseTaskLock(lockPath)

	// leoEnv rides along on every claude invocation for this task (main
	// attempts and the notify-on-fail child alike) — the leo MCP server is
	// always wired into the args by buildArgs now; see leoMCPEnv.
	leoEnv := leoMCPEnv(cfg, taskName)
	// task.Env lets a task target a custom endpoint (ANTHROPIC_BASE_URL/
	// ANTHROPIC_AUTH_TOKEN) or inject other env vars; leoEnv wins on key
	// collision so the leo MCP wiring can never be shadowed by task config.
	// The main attempt loop additionally merges in the resolved harness's own
	// env (e.g. claude's CLAUDE_CODE_ENTRYPOINT) between the two — see
	// runTaskAttempt — but the notify-on-fail child below stays claude-only
	// by construction and adds that env explicitly, so this plain two-way
	// merge is what it uses.
	spawnEnv := mergeEnvMaps(task.Env, leoEnv)

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return fmt.Errorf("assembling prompt: %w", err)
	}

	var sessionID string
	if sessions != nil {
		sid, _, getErr := sessions.Get("task:" + taskName)
		if getErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read session store: %v\n", getErr)
		}
		sessionID = sid
	}

	timeout := cfg.TaskTimeout(task)
	taskWorkspace := cfg.TaskWorkspace(task)

	maxAttempts := task.Retries + 1
	var lastErr error
	var lastOutput []byte
	var lastLogContent string

	// channelPrefixes are the MCP server-name prefixes ("plugin:<name>:")
	// claude reports for this task's configured channel plugins. When one of
	// these fails to initialize, the attempt is aborted so it doesn't burn
	// max_turns talking to a channel that will never deliver.
	channelPrefixes := channelMCPPrefixes(task.Channels, task.DevChannels)
	channelInitAttempts := 0
	isChannelInitRetry := false

	for attempt := 1; attempt <= maxAttempts; {
		if attempt > 1 && !isChannelInitRetry {
			fmt.Fprintf(os.Stderr, "retrying task %q (attempt %d/%d)\n", taskName, attempt, maxAttempts)
			// Clear session for retry attempts — but not when the previous
			// failure was a channel-init flake (errChannelMCPInit): that
			// failure says nothing about the session's validity, so
			// clearing it here would throw away a perfectly good session
			// once the free channel-init retries are exhausted and the
			// failure starts consuming the task's own retry budget.
			if !errors.Is(lastErr, errChannelMCPInit) {
				sessionID = ""
			}
		}
		isChannelInitRetry = false

		ar := runTaskAttempt(cfg, task, taskName, prompt, sessionID, taskWorkspace, timeout, task.Env, leoEnv, channelPrefixes, sessions, h)
		lastLogContent = string(ar.output)

		// A harness that exits 0 while its own stream reports a fatal error
		// (opencode has known exit-0-on-error bugs) must still be treated as
		// a failed attempt — claude exits nonzero on real errors, so this is
		// unreachable there in practice, but it's a real cross-harness signal
		// for the others.
		if ar.execErr == nil && ar.result.IsError {
			ar.execErr = fmt.Errorf("harness reported error: %s", strings.Join(ar.result.Errors, "; "))
		}
		// Session persistence below is deliberately unchanged for this case:
		// an exit-0-but-IsError attempt still persists its non-empty
		// SessionID, exactly like any other failed (nonzero-exit,
		// non-interrupted) attempt — failure semantics mirror nonzero-exit
		// failures.

		// An interrupted attempt (Ctrl-C forwarded to the child) reflects the
		// user asking the task to stop, not a real result — don't persist a
		// session ID out of an aborted attempt's output.
		interrupted := errors.Is(ar.execErr, errInterrupted)

		// Store session ID for next run
		if !interrupted && sessions != nil && ar.result.SessionID != "" {
			if setErr := sessions.Set("task:"+taskName, ar.result.SessionID); setErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to store session ID: %v\n", setErr)
			}
		}

		if ar.execErr == nil {
			lastErr = nil
			lastOutput = nil
			break
		}

		lastErr = ar.execErr
		lastOutput = ar.output

		// Interrupted: stop immediately. No retry, no session clear (handled
		// above and by never re-entering the loop top), no notify-on-fail —
		// see the checks guarding those below.
		if interrupted {
			break
		}

		// A channel plugin's MCP server failing to initialize is an infra
		// flake, not a genuine task failure — retry up to
		// maxChannelInitAttempts times without consuming the task's own
		// retry budget or clearing its session.
		if errors.Is(ar.execErr, errChannelMCPInit) && channelInitAttempts < maxChannelInitAttempts {
			channelInitAttempts++
			isChannelInitRetry = true
			fmt.Fprintf(os.Stderr, "warning: task %q channel plugin MCP init failed (%v); retrying (%d/%d) without consuming retry budget\n",
				taskName, ar.execErr, channelInitAttempts, maxChannelInitAttempts)
			time.Sleep(channelInitBackoff)
			continue
		}

		attempt++
	}

	// Write log for the final attempt only (avoids orphaned files on retries)
	logFile := logFileName(taskName)
	if logErr := writeLogFile(cfg, logFile, []byte(lastLogContent)); logErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write log: %v\n", logErr)
		logFile = ""
	}

	// Record execution history
	exitCode := 0
	reason := history.ReasonSuccess
	if lastErr != nil {
		exitCode = 1
		switch {
		// Derived from lastErr (not a flag captured mid-attempt) so an
		// in-attempt stale-session retry (runTaskAttempt) that itself hits
		// the deadline is correctly reported as a timeout rather than a
		// generic failure — executeCommand's contract is to return ctx.Err()
		// (context.DeadlineExceeded) whenever the deadline fired, on either
		// the initial spawn or the retry.
		case errors.Is(lastErr, errInterrupted):
			reason = history.ReasonInterrupted
		case errors.Is(lastErr, context.DeadlineExceeded):
			reason = history.ReasonTimeout
		case errors.Is(lastErr, errChannelMCPInit):
			reason = history.ReasonChannelInit
		default:
			reason = history.ReasonFailure
		}
	}
	hist := history.NewStore(cfg.HomePath)
	if histErr := hist.Record(taskName, exitCode, reason, logFile); histErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to record history: %v\n", histErr)
	}

	// Send failure notification if configured (via child claude invocation).
	// Skipped when interrupted: the user asked the task to stop, so firing a
	// notify-on-fail child (itself a claude invocation) would be surprising
	// and unwanted, not helpful.
	if lastErr != nil && !errors.Is(lastErr, errInterrupted) && task.NotifyOnFail && len(task.Channels) > 0 {
		notifyFailure(cfg, taskName, task, taskWorkspace, lastErr, maxAttempts, spawnEnv, logFile)
	}

	if lastErr != nil {
		if errors.Is(lastErr, errInterrupted) {
			return fmt.Errorf("task %q was interrupted: %w", taskName, lastErr)
		}
		return fmt.Errorf("claude exited with error: %w\nOutput: %s", lastErr, string(lastOutput))
	}

	return nil
}

// attemptResult captures the outcome of one main-loop task attempt,
// including any in-place stale-session retry (see runTaskAttempt). Whether
// the attempt timed out is derived by the caller from execErr via
// errors.Is(execErr, context.DeadlineExceeded) — see executeCommand's
// contract — rather than captured as a separate flag here, since a flag
// snapshotted before the in-place stale-session retry would miss a deadline
// that fires during that retry instead of the initial spawn.
type attemptResult struct {
	output  []byte
	execErr error
	result  harness.Result
}

// runTaskAttempt spawns the resolved harness for one attempt of the task,
// retrying in-place (same attempt, no session clear) without session-resume
// if the initial spawn used a session that turns out to be stale.
//
// Spawn env is assembled fresh on each buildArgs call (initial spawn and any
// stale-session retry) as mergeEnvMaps(harnessEnv, taskEnv, leoEnv): harnessEnv
// is the base, taskEnv may deliberately override it (e.g. claude's
// CLAUDE_CODE_ENTRYPOINT), and leoEnv wins on collision last so a task can
// never shadow the leo MCP wiring.
func runTaskAttempt(cfg *config.Config, task config.TaskConfig, taskName, prompt, sessionID, taskWorkspace string, timeout time.Duration, taskEnv, leoEnv map[string]string, channelPrefixes []string, sessions *session.Store, h harness.Harness) attemptResult {
	args, harnessEnv := buildArgs(cfg, task, taskName, prompt, sessionID, leoEnv)
	spawnEnv := mergeEnvMaps(harnessEnv, taskEnv, leoEnv)

	// Per-attempt timeout so each retry gets the full timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	output, execErr := executeCommand(ctx, h.Binary(), taskWorkspace, args, task.Channels, task.DevChannels, spawnEnv, channelPrefixes)
	result, _ := h.ParseEvents(bytes.NewReader(output))

	// If session-resume failed with a stale session, retry without it.
	if execErr != nil && sessionID != "" && isSessionError(result, output) {
		if sessions != nil {
			if delErr := sessions.Delete("task:" + taskName); delErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to clear stale session: %v\n", delErr)
			}
		}

		args, harnessEnv = buildArgs(cfg, task, taskName, prompt, "", leoEnv)
		spawnEnv = mergeEnvMaps(harnessEnv, taskEnv, leoEnv)
		output, execErr = executeCommand(ctx, h.Binary(), taskWorkspace, args, task.Channels, task.DevChannels, spawnEnv, channelPrefixes)
		result, _ = h.ParseEvents(bytes.NewReader(output))
	}

	return attemptResult{output: output, execErr: execErr, result: result}
}

// notifyFailure spawns a short, bounded claude invocation that asks the agent
// to deliver a failure notification via one of the task's configured channel
// plugins. All errors are logged and swallowed so notify failures don't cascade
// back to the parent task. Its output is appended to the task's log file
// (when logFile is non-empty) so operators can see what the notify child
// actually did. A single fresh-spawn retry is attempted on failure before
// giving up.
func notifyFailure(cfg *config.Config, taskName string, task config.TaskConfig, workspace string, taskErr error, attempts int, extraEnv map[string]string, logFile string) {
	prompt := fmt.Sprintf(
		"Task %q failed after %d attempt(s): %v.\n"+
			"Use a messaging tool from one of your configured channel plugins (see $LEO_CHANNELS) "+
			"to deliver this failure notification to the user. Keep it concise.\n"+
			"When done, reply NO_REPLY.",
		taskName, attempts, taskErr,
	)

	args := []string{
		"-p", prompt,
		"--max-turns", "3",
		"--permission-mode", "acceptEdits",
		"--output-format", "text",
	}
	args = appendDevChannelFlags(args, task.DevChannels)

	// notifyFailure stays claude-only by construction: it fires only for
	// channel tasks, and channels validate claude-only (see SupportsChannels).
	// executeCommand no longer injects CLAUDE_CODE_ENTRYPOINT itself (that
	// now lives in claude's own Env()), so it's added here explicitly.
	notifyEnv := mergeEnvMaps(map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, extraEnv)

	spawn := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), notifyFailureTimeout)
		defer cancel()
		// No channel-init monitoring for the notify child: it's a short,
		// low-stakes invocation and killing it early would only reduce the
		// chance of the failure notice actually reaching the user.
		return executeCommand(ctx, claudeharness.Claude{}.Binary(), workspace, args, task.Channels, task.DevChannels, notifyEnv, nil)
	}

	output, err := spawn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: notify-on-fail child invocation failed: %v; retrying once\n", err)
		var retryOutput []byte
		retryOutput, err = spawn()
		if len(retryOutput) > 0 {
			output = retryOutput
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: notify-on-fail child invocation failed after retry: %v\n", err)
		}
	}

	if logFile != "" && len(output) > 0 {
		appendNotifyOutputToLog(cfg, logFile, output)
	}
}

// appendNotifyOutputToLog appends the notify-on-fail child's captured output
// to the task's log file, separated by a marker line. Errors are logged and
// swallowed — the notify child already ran; failing to log it shouldn't
// surface as a task failure.
func appendNotifyOutputToLog(cfg *config.Config, logFile string, output []byte) {
	logPath := filepath.Join(cfg.StatePath(), "logs", logFile)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to append notify-on-fail output to log: %v\n", err)
		return
	}
	defer f.Close()

	if _, err := f.WriteString(notifyOutputSeparator); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to append notify-on-fail output to log: %v\n", err)
		return
	}
	if _, err := f.Write(output); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to append notify-on-fail output to log: %v\n", err)
	}
}

// isSessionError checks whether a claude failure was caused by an invalid/stale
// session. It inspects the parsed result text, each entry of the "errors"
// array (populated on subtype "error_during_execution" results, which carry
// no "result" field), and finally the raw combined output as a last resort.
func isSessionError(result harness.Result, output []byte) bool {
	candidates := make([]string, 0, len(result.Errors)+1)
	candidates = append(candidates, result.Text)
	candidates = append(candidates, result.Errors...)

	for _, c := range candidates {
		if sessionErrorText(c) {
			return true
		}
	}

	// Last resort: scan the raw combined output, but only when the parsed
	// result carried no text of its own (claude never emitted a usable
	// result/errors field — e.g. it crashed before producing a result
	// event). Otherwise ordinary conversation text that happens to mention
	// "session" alongside "expired"/"invalid"/"not found" could
	// false-trigger a stale-session clear even though the parsed result
	// says nothing of the kind.
	if result.Text == "" && len(result.Errors) == 0 && sessionErrorText(string(output)) {
		return true
	}
	return false
}

// sessionErrorText reports whether text (case-insensitively) matches a known
// stale/invalid-session error pattern.
func sessionErrorText(text string) bool {
	text = strings.ToLower(text)
	if text == "" {
		return false
	}
	if strings.Contains(text, "no conversation found") {
		return true
	}
	// codex stale-thread resume failures say "thread", never "session" (e.g.
	// "thread/resume failed: no rollout found for thread id …"), so they
	// never match the generic session+not-found pattern below.
	if strings.Contains(text, "no rollout found") {
		return true
	}
	return strings.Contains(text, "session") &&
		(strings.Contains(text, "not found") || strings.Contains(text, "invalid") || strings.Contains(text, "expired"))
}

// syncBuffer is a mutex-guarded bytes.Buffer safe for concurrent writes from
// the stdout- and stderr-copying goroutines os/exec spawns internally.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Copy out to avoid returning a slice aliasing the internal buffer,
	// which could still be mutated concurrently by a slow-to-exit copier.
	out := make([]byte, s.buf.Len())
	copy(out, s.buf.Bytes())
	return out
}

// maxScannerBufferSize bounds the channel-init monitor's per-line scan
// buffer. claude stream-json lines carrying large tool results are normal
// and can comfortably exceed a small cap, so this is generous; lines beyond
// it simply stop being scanned for channel-init events (see the drain in
// executeCommand, which covers that case so the child is never blocked
// writing). A package-level var (not const) so tests can shrink it to
// exercise the overflow path without generating multi-megabyte fixtures.
var maxScannerBufferSize = 10 * 1024 * 1024

// executeCommand spawns claude and returns its combined stdout+stderr output.
// When channelInitPrefixes is non-empty, the child's stdout is additionally
// scanned incrementally (line by line) for the stream-json system/init event;
// if a configured channel plugin's MCP server reports status "failed" there,
// the process is killed immediately and the returned error wraps
// errChannelMCPInit — the caller doesn't have to wait out the full timeout
// for a channel that will never come up. Pass nil to skip monitoring (e.g.
// for the notify-on-fail child, where it isn't worth the complexity).
func executeCommand(ctx context.Context, binary, workDir string, args []string, channels, devChannels []string, extraEnv map[string]string, channelInitPrefixes []string) ([]byte, error) {
	cmd := execCommand(binary, args...)
	cmd.Dir = workDir
	env := os.Environ()
	if len(channels) > 0 {
		env = append(env, "LEO_CHANNELS="+strings.Join(channels, ","))
	}
	if len(devChannels) > 0 {
		env = append(env, "LEO_DEV_CHANNELS="+strings.Join(devChannels, ","))
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	// stdout is wrapped in a mutex because when channel-init monitoring is
	// active, cmd.Stdout becomes an io.MultiWriter (a distinct Writer value
	// from cmd.Stderr) — os/exec only serializes concurrent writes to a
	// shared output when Stdout and Stderr are the *same* Writer value, so
	// without the lock the stdout- and stderr-copying goroutines it spawns
	// internally would race on the underlying buffer.
	stdout := &syncBuffer{}
	cmd.Stderr = stdout

	var pw *io.PipeWriter
	var pr *io.PipeReader
	if len(channelInitPrefixes) > 0 {
		pr, pw = io.Pipe()
		cmd.Stdout = io.MultiWriter(stdout, pw)
	} else {
		cmd.Stdout = stdout
	}
	// Always run in its own process group. A kill (context deadline, a
	// monitor-detected failed channel MCP server) or a forwarded interactive
	// SIGINT/SIGTERM needs to reach any children claude spawned that
	// inherited its stdout/stdin — otherwise an orphaned grandchild can keep
	// the pipe open (cmd.Wait() would hang waiting for EOF that never comes)
	// or outlive the parent's own signal handling.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// done is closed once cmd.Wait() returns below, i.e. once the child has
	// been reaped. Every goroutine that might signal the child checks done
	// immediately before doing so: without that guard, a kill/signal racing
	// with natural process exit could — after PID reuse by the OS — hit an
	// unrelated process group instead of the (already-gone) child.
	done := make(chan struct{})

	signalGroup := func(sig syscall.Signal) {
		// Residual TOCTOU: the done-check above and the syscall.Kill below
		// are not atomic with each other. A race remains in principle — done
		// could close (the child gets reaped and its PID/pgid released back
		// to the OS) in the gap between this check and the Kill call below,
		// and if the OS reissues that exact PID/pgid to a new, unrelated
		// process in that same narrow window, the signal would be delivered
		// to the wrong process group. This is not fixable portably: POSIX
		// kill(2) has no "signal this pid only if it's still the process I
		// think it is" primitive (Linux's pidfd_send_signal closes the
		// window but isn't portable, and isn't worth the platform-specific
		// complexity here). The done-check still shrinks the window from
		// "any time after the child exits" down to a handful of
		// instructions, making the race negligible in practice.
		select {
		case <-done:
			return
		default:
		}
		if cmd.Process == nil {
			return
		}
		if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
			return
		}
		// -pgid delivery failed; fall back to signaling just the direct
		// child rather than delivering nothing at all.
		_ = cmd.Process.Signal(sig)
	}
	// kill delivers SIGKILL to the whole process group and keeps re-sending
	// it briefly in the background. A single SIGKILL only reaches processes
	// that already exist in the group at that instant — if claude (or, in
	// tests, a shell script) forks a child a few microseconds after we
	// signal, that straggler never receives it and can run to completion
	// holding the stdout pipe open, which would otherwise hang cmd.Wait()
	// indefinitely despite the "kill" having "succeeded". Re-sending at a
	// short interval until done closes (i.e. until Wait() actually returns,
	// which by construction requires every pipe holder to be gone) closes
	// that window; the resend loop is itself bounded so it can never leak
	// past a couple of seconds even in some unforeseen pathological case.
	kill := func() {
		signalGroup(syscall.SIGKILL)
		go func() {
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			giveUp := time.After(2 * time.Second)
			for {
				select {
				case <-done:
					return
				case <-giveUp:
					return
				case <-ticker.C:
					signalGroup(syscall.SIGKILL)
				}
			}
		}()
	}

	// forwardInterrupt delivers a forwarded SIGINT/SIGTERM to the child's
	// process group and keeps re-sending it until the child is reaped,
	// escalating to SIGKILL once the grace period expires.
	//
	// A single kill(-pgid) only reaches the processes in the group at that
	// instant, and the one that actually matters may not exist yet: an
	// interrupt arriving just after cmd.Start() lands before the child has
	// forked the worker that holds stdout open. The parent may well survive it
	// — a shell waiting on a foreground child *catches* SIGINT rather than
	// dying — and then forks a worker that never receives anything, runs to
	// completion holding the pipe, and blocks cmd.Wait() for its full
	// lifetime. Re-sending closes that window; the SIGKILL escalation
	// guarantees an interrupt can never hang the caller outright. This mirrors
	// kill()'s resend loop, which exists for the very same race.
	forwardInterrupt := func(sig syscall.Signal) {
		signalGroup(sig)
		go func() {
			ticker := time.NewTicker(interruptResendInterval)
			defer ticker.Stop()
			escalate := time.After(interruptGracePeriod)
			for {
				select {
				case <-done:
					return
				case <-escalate:
					kill()
					return
				case <-ticker.C:
					signalGroup(sig)
				}
			}
		}()
	}

	// Monitor context in background; kill process if deadline expires.
	go func() {
		select {
		case <-ctx.Done():
			kill()
		case <-done:
		}
	}()

	// Forward interactive SIGINT/SIGTERM (e.g. Ctrl-C on `leo run`) to the
	// child's process group. Setpgid above detaches the child from the
	// terminal's foreground process group, so without this it would never
	// see the signal and would be left running after the parent exits.
	//
	// interrupted is set before forwarding so that once cmd.Wait() returns
	// (necessarily after the forwarded signal reached and killed the child —
	// real wall-clock ordering, backed by atomic Store/Load for cross-
	// goroutine visibility), the caller below can distinguish "we forwarded a
	// signal that killed this child" from an ordinary child failure and
	// return errInterrupted instead.
	var interrupted atomic.Bool
	var forwardOnce sync.Once
	sigCh := make(chan os.Signal, 1)
	signalNotifyFn(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		defer signal.Stop(sigCh)
		for {
			select {
			case sig := <-sigCh:
				if s, ok := sig.(syscall.Signal); ok {
					interrupted.Store(true)
					// Only the first signal starts a resend loop; later ones
					// are forwarded directly so repeated Ctrl-C can't stack
					// redundant loops on top of the one already running.
					started := false
					forwardOnce.Do(func() {
						started = true
						forwardInterrupt(s)
					})
					if !started {
						signalGroup(s)
					}
				}
			case <-done:
				return
			}
		}
	}()

	var monitorErr error
	var monitorDone chan struct{}
	if pw != nil {
		monitorDone = make(chan struct{})
		go func() {
			defer close(monitorDone)
			initialBufSize := 64 * 1024
			if initialBufSize > maxScannerBufferSize {
				initialBufSize = maxScannerBufferSize
			}
			scanner := bufio.NewScanner(pr)
			scanner.Buffer(make([]byte, 0, initialBufSize), maxScannerBufferSize)
			for scanner.Scan() {
				if monitorErr != nil {
					continue // already found a failure; keep draining so writes don't block
				}
				if name, failed := failedChannelMCPServer(scanner.Bytes(), channelInitPrefixes); failed {
					monitorErr = fmt.Errorf("%w: %s", errChannelMCPInit, name)
					kill()
				}
			}
			// The scan loop can end early — e.g. bufio.ErrTooLong on a
			// single line bigger than maxScannerBufferSize — while the
			// child is still writing. Nothing else reads pr in that case,
			// so the MultiWriter's write into pw would block forever, the
			// os/exec-internal stdout copier would never finish, and
			// cmd.Wait() below would hang even after kill(). Draining until
			// pw.Close() (called after Wait returns) guarantees that never
			// happens, at the cost of no longer scanning for channel-init
			// events past the oversized line — an acceptable trade since
			// that's already a pathological case.
			io.Copy(io.Discard, pr) //nolint:errcheck // draining only; errors are expected once pw closes
		}()
	}

	err := cmd.Wait()
	close(done) // stop the ctx-watcher and signal-forwarding goroutines

	if pw != nil {
		_ = pw.Close() // error is always nil for io.PipeWriter; unblocks the drain/scanner goroutine (EOF) once the child has exited
		<-monitorDone
	}

	if monitorErr != nil {
		return stdout.Bytes(), monitorErr
	}
	if ctx.Err() != nil {
		return stdout.Bytes(), ctx.Err()
	}
	if err != nil && interrupted.Load() {
		return stdout.Bytes(), fmt.Errorf("%w: %v", errInterrupted, err)
	}
	return stdout.Bytes(), err
}

// mcpServerStatus mirrors one entry in stream-json's system/init event
// "mcp_servers" array.
type mcpServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// initSystemEvent is the minimal shape of stream-json's first event (type
// "system", subtype "init"), which reports each MCP server's startup status.
type initSystemEvent struct {
	Type       string            `json:"type"`
	Subtype    string            `json:"subtype"`
	MCPServers []mcpServerStatus `json:"mcp_servers"`
}

// failedChannelMCPServer inspects a single stream-json output line; if it is
// the system/init event and one of its mcp_servers matches a configured
// channel prefix ("plugin:<name>:") with status "failed", it returns that
// server's full name.
func failedChannelMCPServer(line []byte, channelInitPrefixes []string) (string, bool) {
	if len(channelInitPrefixes) == 0 {
		return "", false
	}
	var evt initSystemEvent
	if json.Unmarshal(line, &evt) != nil {
		return "", false
	}
	if evt.Type != "system" || evt.Subtype != "init" {
		return "", false
	}
	for _, srv := range evt.MCPServers {
		if srv.Status != "failed" {
			continue
		}
		for _, prefix := range channelInitPrefixes {
			if strings.HasPrefix(srv.Name, prefix) {
				return srv.Name, true
			}
		}
	}
	return "", false
}

// channelMCPPrefixes converts configured channel plugin ids
// ("plugin:<name>@<marketplace>") into the MCP server-name prefixes claude
// reports them under ("plugin:<name>:<server-name>"). Duplicate prefixes are
// collapsed; channel ids that don't parse as "plugin:<name>@..." (e.g. any
// future non-plugin channel kind) are skipped since they have no MCP server
// counterpart to watch.
func channelMCPPrefixes(channelSets ...[]string) []string {
	seen := make(map[string]bool)
	var prefixes []string
	for _, channels := range channelSets {
		for _, ch := range channels {
			name, ok := channelPluginName(ch)
			if !ok {
				continue
			}
			prefix := "plugin:" + name + ":"
			if seen[prefix] {
				continue
			}
			seen[prefix] = true
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

// channelPluginName extracts <name> from a channel id of the form
// "plugin:<name>@<marketplace>". Returns ok=false for anything else.
func channelPluginName(channel string) (string, bool) {
	rest, ok := strings.CutPrefix(channel, "plugin:")
	if !ok {
		return "", false
	}
	name, _, ok := strings.Cut(rest, "@")
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

func assemblePrompt(cfg *config.Config, task config.TaskConfig) (string, error) {
	taskWorkspace := cfg.TaskWorkspace(task)

	absPrompt, err := config.ResolvePromptPath(taskWorkspace, task.PromptFile)
	if err != nil {
		return "", err
	}

	promptData, err := os.ReadFile(absPrompt)
	if err != nil {
		return "", fmt.Errorf("reading prompt file %s: %w", absPrompt, err)
	}

	var parts []string

	if task.Silent {
		parts = append(parts, silentPreamble)
	}

	parts = append(parts, string(promptData))

	return strings.Join(parts, "\n"), nil
}

// leoMCPEnv builds the LEO_PROCESS_NAME / LEO_WEB_PORT / LEO_API_TOKEN
// environment that `leo mcp-server` (internal/mcp/server.go) uses to bind
// itself to this task and, when a daemon token is available, authenticate
// against the daemon's web API (see internal/service/process.go, which
// exports the same three vars for supervised processes). The leo MCP server
// is always wired in for every task; LEO_WEB_PORT/LEO_API_TOKEN are simply
// absent/empty when the web UI is disabled or no readable, non-empty API
// token file exists, in which case the server self-selects local-only mode
// (only the local leo_skill tool is served).
func leoMCPEnv(cfg *config.Config, taskName string) map[string]string {
	env := map[string]string{
		"LEO_PROCESS_NAME": "task:" + taskName,
	}
	if cfg == nil {
		return env
	}
	env["LEO_WEB_PORT"] = strconv.Itoa(cfg.WebPort())
	if !cfg.Web.Enabled {
		return env
	}
	// api.token lives at <state>/api.token; see web.APITokenPath, which
	// owns the canonical path (and file-creation logic) for this file.
	data, err := os.ReadFile(web.APITokenPath(cfg.StatePath()))
	if err != nil {
		return env
	}
	if token := strings.TrimSpace(string(data)); token != "" {
		env["LEO_API_TOKEN"] = token
	}
	return env
}

// mergeEnvMaps combines any number of env maps into one, later maps taking
// precedence on key collision. Returns nil (not an empty map) when the
// result would be empty, so callers checking len(extraEnv) == 0 still work.
func mergeEnvMaps(maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildArgs resolves the task's harness, fills in its runtime-only options
// (leo MCP wiring, resolved session state, etc.), and renders both the argv
// and the harness's own extra spawn env for one launch. leoEnv carries the
// LEO_* vars from leoMCPEnv, always non-nil now — the leo MCP server is
// always wired in for every harness, with LEO_WEB_PORT/LEO_API_TOKEN simply
// absent/empty when web is disabled or no token is available.
func buildArgs(cfg *config.Config, task config.TaskConfig, taskName, prompt, sessionID string, leoEnv map[string]string) ([]string, map[string]string) {
	h, err := harness.Get(cfg.TaskHarness(task))
	if err != nil {
		log.Printf("[task:%s] resolving harness: %v", taskName, err)
		return nil, nil
	}
	decoded, err := h.DecodeOptions(cfg.TaskHarnessOptions(task))
	if err != nil {
		log.Printf("[task:%s] decoding harness options: %v", taskName, err)
		return nil, nil
	}

	sess := harness.SessionState{}
	if sessionID != "" {
		sess = harness.SessionState{Mode: harness.SessionResume, ID: sessionID}
	}
	spec := harness.LaunchSpec{
		Kind:          harness.KindTask,
		Name:          taskName,
		Model:         cfg.TaskModel(task),
		MaxTurns:      cfg.TaskMaxTurns(task),
		Workspace:     cfg.TaskWorkspace(task),
		DevChannels:   task.DevChannels,
		Prompt:        prompt,
		Session:       sess,
		SystemContext: leomcp.LeoNudge(cfg),
	}

	switch opts := decoded.(type) {
	case claudeharness.Options:
		mcpConfig := ""
		if p := cfg.TaskMCPConfigPath(task); config.HasMCPServers(p) {
			mcpConfig = p
		}
		opts.MCPConfigPath = mcpConfig
		opts.LeoMCPArgs = leomcp.AppendArg(nil, cfg)
		spec.Options = opts
	case codexharness.Options:
		opts.LeoMCP = &codexharness.LeoMCPBridge{
			Command:      "leo",
			Args:         []string{"mcp-server"},
			EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
			ApprovalMode: "approve",
		}
		spec.Options = opts
	case opencodeharness.Options:
		opts.LeoMCP = &opencodeharness.LeoMCPBridge{
			Command: []string{"leo", "mcp-server"},
			Env:     leoEnv,
		}
		spec.Options = opts
	default:
		log.Printf("[task:%s] harness %q returned unsupported options type %T", taskName, h.Name(), decoded)
		return nil, nil
	}

	args, err := h.Args(spec)
	if err != nil {
		log.Printf("[task:%s] building %s args: %v", taskName, h.Name(), err)
		return nil, nil
	}
	env, err := h.Env(spec)
	if err != nil {
		log.Printf("[task:%s] building %s env: %v", taskName, h.Name(), err)
		return nil, nil
	}
	return args, env
}

// appendDevChannelFlags appends one --dangerously-load-development-channels
// flag per dev channel. Used by both buildArgs and notifyFailure so the flag
// wiring lives in one place.
func appendDevChannelFlags(args []string, devChannels []string) []string {
	for _, ch := range devChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}
	return args
}

// logFileName returns a timestamped log filename for the current run.
func logFileName(taskName string) string {
	return fmt.Sprintf("%s-%s.log", taskName, time.Now().UTC().Format("20060102-150405.000"))
}

func writeLogFile(cfg *config.Config, filename string, output []byte) error {
	logDir := filepath.Join(cfg.StatePath(), "logs")
	if err := os.MkdirAll(logDir, 0750); err != nil {
		return err
	}

	logPath := filepath.Join(logDir, filename)
	return os.WriteFile(logPath, output, 0600)
}

// acquireTaskLock creates an exclusive lock file to prevent concurrent task execution.
// If a lock file exists but the owning process is dead, the stale lock is removed.
func acquireTaskLock(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			// Check if the lock is stale (owning process is dead)
			data, readErr := os.ReadFile(path)
			if readErr == nil {
				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if parseErr == nil {
					proc, findErr := os.FindProcess(pid)
					if findErr == nil && proc.Signal(syscall.Signal(0)) != nil {
						// Process is dead, remove stale lock and retry once
						os.Remove(path)
						return acquireTaskLock(path)
					}
				}
			}
			return fmt.Errorf("lock file exists at %s", path)
		}
		return err
	}

	if _, err := fmt.Fprintf(f, "%d", os.Getpid()); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("writing pid to lock file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("closing lock file %s: %w", path, err)
	}
	return nil
}

// releaseTaskLock removes the lock file.
func releaseTaskLock(path string) {
	os.Remove(path)
}
