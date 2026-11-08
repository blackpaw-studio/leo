package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/session"
)

// sessionRig runs one task through Run with a stubbed claude that mints
// "sess-<n>" session ids and records the --resume id (or "" for a fresh
// start) of every spawn.
type sessionRig struct {
	t        *testing.T
	cfg      *config.Config
	sessions *session.Store
	spawns   []string
	// failOn, when it returns true for a 1-based spawn number, makes that
	// spawn exit 1 after printing its result event.
	failOn func(n int) bool
}

func newSessionRig(t *testing.T, task config.TaskConfig) *sessionRig {
	t.Helper()
	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	task.PromptFile = "task.md"
	task.Schedule = "0 * * * *"
	task.Enabled = true
	r := &sessionRig{
		t: t,
		cfg: &config.Config{
			HomePath: dir,
			Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			Tasks:    map[string]config.TaskConfig{"mytask": task},
		},
		sessions: session.NewStore(dir),
	}
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		resumeID := ""
		for i, a := range args {
			if a == "--resume" && i+1 < len(args) {
				resumeID = args[i+1]
			}
		}
		r.spawns = append(r.spawns, resumeID)
		n := len(r.spawns)
		script := fmt.Sprintf(`echo '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"sess-%d","result":"ok"}'`, n)
		if r.failOn != nil && r.failOn(n) {
			script += "; exit 1"
		}
		return exec.Command("sh", "-c", script)
	}
	return r
}

func (r *sessionRig) run() error {
	r.t.Helper()
	return Run(r.cfg, "mytask", r.sessions, leomcp.Server{})
}

func (r *sessionRig) stored() session.Entry {
	r.t.Helper()
	e, _, err := r.sessions.Lookup("task:mytask")
	if err != nil {
		r.t.Fatalf("Lookup: %v", err)
	}
	return e
}

func TestRunFreshSessionIgnoresStoredIDAndWritesNothing(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{Session: "fresh"})
	if err := r.sessions.SetRuns("task:mytask", "old-session", 2); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := r.run(); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	for i, id := range r.spawns {
		if id != "" {
			t.Errorf("spawn %d resumed %q, want a fresh start", i+1, id)
		}
	}
	// The pre-existing key is ignored, not deleted or overwritten.
	if got := r.stored(); got.SessionID != "old-session" || got.Runs != 2 {
		t.Errorf("stored entry = %+v, want untouched old-session/2", got)
	}
}

func TestRunFreshSessionLeavesNoKeyBehind(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{Session: "fresh"})
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := r.sessions.Lookup("task:mytask"); found {
		t.Error("fresh run stored a session key")
	}
}

func TestPreviewFreshSessionOmitsResume(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{Session: "fresh"})
	if err := r.sessions.Set("task:mytask", "old-session"); err != nil {
		t.Fatal(err)
	}
	_, args, err := Preview(r.cfg, "mytask", r.sessions, leomcp.Server{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "--resume") {
		t.Errorf("fresh preview args contain --resume: %v", args)
	}
}

func TestRunRotationStartsFreshAfterNRuns(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{SessionRotateAfter: 3})
	for i := 0; i < 4; i++ {
		if err := r.run(); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	// Run 1 is fresh; 2 and 3 resume sess-1's lineage; run 4 rotates.
	want := []string{"", "sess-1", "sess-2", ""}
	if strings.Join(r.spawns, ",") != strings.Join(want, ",") {
		t.Errorf("resume ids = %q, want %q", r.spawns, want)
	}
	if got := r.stored(); got.SessionID != "sess-4" || got.Runs != 1 {
		t.Errorf("stored after rotation = %+v, want sess-4 with 1 run", got)
	}
}

func TestRunRotationTreatsLegacyEntryAsZeroRuns(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{SessionRotateAfter: 2})
	if err := r.sessions.Set("task:mytask", "legacy"); err != nil { // no count
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := r.run(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"legacy", "sess-1", ""}
	if strings.Join(r.spawns, ",") != strings.Join(want, ",") {
		t.Errorf("resume ids = %q, want %q", r.spawns, want)
	}
}

func TestRunRotationDisabledNeverRotates(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{})
	if err := r.sessions.SetRuns("task:mytask", "keep", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if r.spawns[0] != "keep" {
		t.Errorf("resumed %q, want keep", r.spawns[0])
	}
}

func TestPreviewRotationDueOmitsResume(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{SessionRotateAfter: 2})
	if err := r.sessions.SetRuns("task:mytask", "full", 2); err != nil {
		t.Fatal(err)
	}
	_, args, err := Preview(r.cfg, "mytask", r.sessions, leomcp.Server{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "--resume") {
		t.Errorf("rotated preview args contain --resume: %v", args)
	}
}

func TestRunRetryClearResetsRunCount(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{SessionRotateAfter: 5, Retries: 1})
	r.failOn = func(n int) bool { return n == 1 }
	if err := r.sessions.SetRuns("task:mytask", "seed", 3); err != nil {
		t.Fatal(err)
	}
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	// Attempt 1 resumes the seed and fails; attempt 2 starts fresh.
	if want := []string{"seed", ""}; strings.Join(r.spawns, ",") != strings.Join(want, ",") {
		t.Fatalf("resume ids = %q, want %q", r.spawns, want)
	}
	if got := r.stored(); got.SessionID != "sess-2" || got.Runs != 1 {
		t.Errorf("stored after retry clear = %+v, want sess-2 with 1 run", got)
	}
}

func TestRunStaleSessionRecoveryResetsRunCount(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{SessionRotateAfter: 5})
	stale := `No conversation found with session ID: stale-id` + "\n" +
		`{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"session_id":"stale-id","errors":["No conversation found with session ID: stale-id"]}`
	fresh := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"recovered","result":"ok"}`
	execCommand = func(name string, args ...string) *exec.Cmd {
		if strings.Contains(strings.Join(args, " "), "--resume") {
			return exec.Command("sh", "-c", "printf '%s\\n' \"$0\"; exit 1", stale)
		}
		return exec.Command("sh", "-c", "printf '%s\\n' \"$0\"", fresh)
	}
	if err := r.sessions.SetRuns("task:mytask", "stale-id", 4); err != nil {
		t.Fatal(err)
	}
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if got := r.stored(); got.SessionID != "recovered" || got.Runs != 1 {
		t.Errorf("stored after stale recovery = %+v, want recovered with 1 run", got)
	}
}

func TestRunInterruptedAttemptDoesNotCountTowardRotation(t *testing.T) {
	stubFailureCards(t, nil)
	r := newSessionRig(t, config.TaskConfig{SessionRotateAfter: 5})
	if err := r.sessions.SetRuns("task:mytask", "seed", 2); err != nil {
		t.Fatal(err)
	}

	registered := make(chan chan<- os.Signal, 8)
	origNotify := signalNotifyFn
	t.Cleanup(func() { signalNotifyFn = origNotify })
	signalNotifyFn = func(c chan<- os.Signal, sig ...os.Signal) { registered <- c }
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", `echo '{"type":"result","session_id":"sess-int"}'; sleep 30`)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- r.run() }()
	select {
	case c := <-registered:
		c <- syscall.SIGINT
	case <-time.After(5 * time.Second):
		t.Fatal("executeCommand never registered a signal handler")
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected an interrupt error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the interrupt")
	}

	if got := r.stored(); got.SessionID != "seed" || got.Runs != 2 {
		t.Errorf("stored after interrupt = %+v, want untouched seed/2", got)
	}
}

func TestRunChannelInitRetryCountsOncePerFiring(t *testing.T) {
	r := newSessionRig(t, config.TaskConfig{
		SessionRotateAfter: 5,
		Channels:           []string{"plugin:blackpaw-telegram@blackpaw-studio/claude-plugins"},
	})
	origBackoff := channelInitBackoff
	channelInitBackoff = 0
	t.Cleanup(func() { channelInitBackoff = origBackoff })
	if err := r.sessions.SetRuns("task:mytask", "seed", 2); err != nil {
		t.Fatal(err)
	}

	initFailed := `{"type":"system","subtype":"init","mcp_servers":[{"name":"plugin:blackpaw-telegram:blackpaw-telegram","status":"failed"}]}`
	result := func(id string) string {
		return fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"%s","result":"ok"}`, id)
	}
	n := 0
	execCommand = func(name string, args ...string) *exec.Cmd {
		n++
		if n == 1 {
			return exec.Command("sh", "-c", "printf '%s\\n' \"$0\" \"$1\"; sleep 5", initFailed, result("sess-1"))
		}
		return exec.Command("sh", "-c", "printf '%s\\n' \"$0\"", result("sess-2"))
	}

	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("spawns = %d, want 2", n)
	}
	if got := r.stored(); got.SessionID != "sess-2" || got.Runs != 3 {
		t.Errorf("stored = %+v, want sess-2 with 3 runs (prior 2 + one firing)", got)
	}
}
