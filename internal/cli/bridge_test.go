package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
)

// fakeBridge records what the bridge command asked of the daemon.
type fakeBridge struct {
	mu          sync.Mutex
	streamBody  io.ReadCloser
	streamErr   error
	streamAgent string
	streamHome  string
	reportAgent string
	reportHome  string
	reportBody  []byte
	reportErr   error
	reports     int
	parentGone  chan struct{}
}

func (f *fakeBridge) deps(env map[string]string) bridgeDeps {
	if f.parentGone == nil {
		f.parentGone = make(chan struct{})
	}
	return bridgeDeps{
		openStream: func(_ context.Context, home, agent string) (io.ReadCloser, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.streamHome, f.streamAgent = home, agent
			if f.streamErr != nil {
				return nil, f.streamErr
			}
			return f.streamBody, nil
		},
		postReport: func(_ context.Context, home, agent string, body []byte) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.reports++
			f.reportHome, f.reportAgent, f.reportBody = home, agent, append([]byte(nil), body...)
			return f.reportErr
		},
		getenv:     func(k string) string { return env[k] },
		homeDir:    func() string { return "/leo-home" },
		parentGone: func(context.Context) <-chan struct{} { return f.parentGone },
	}
}

func runBridgeCmd(t *testing.T, deps bridgeDeps, out io.Writer, args ...string) error {
	t.Helper()
	return runBridgeCmdWithStdin(t, deps, strings.NewReader(""), out, args...)
}

func runBridgeCmdWithStdin(t *testing.T, deps bridgeDeps, in io.Reader, out io.Writer, args ...string) error {
	t.Helper()
	cmd := newBridgeCmdWith(deps)
	cmd.SetArgs(args)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cmd.ExecuteContext(ctx)
}

func streamOf(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

// lineWriter records each Write and Flush, to pin one write per command.
type lineWriter struct {
	writes  []string
	flushes int
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *lineWriter) Flush() error { w.flushes++; return nil }

func TestBridgeStreamCopiesEachLineAndExitsCleanly(t *testing.T) {
	f := &fakeBridge{streamBody: streamOf("{\"id\":\"c1\"}\n{\"id\":\"c2\"}\n")}
	out := &lineWriter{}
	if err := runBridgeCmd(t, f.deps(nil), out, "--agent", "leo-alpha"); err != nil {
		t.Fatalf("a clean end of stream must exit 0, got %v", err)
	}
	want := []string{"{\"id\":\"c1\"}\n", "{\"id\":\"c2\"}\n"}
	if strings.Join(out.writes, "|") != strings.Join(want, "|") {
		t.Fatalf("writes=%q, want one write per line %q", out.writes, want)
	}
	if out.flushes != len(want) {
		t.Fatalf("flushes=%d, want one per line", out.flushes)
	}
	if f.streamAgent != "leo-alpha" || f.streamHome != "/leo-home" {
		t.Fatalf("connected as %q in %q, want leo-alpha in /leo-home", f.streamAgent, f.streamHome)
	}
}

func TestBridgeStreamCopiesLongLinesWhole(t *testing.T) {
	long := "{\"text\":\"" + strings.Repeat("x", 200<<10) + "\"}\n"
	f := &fakeBridge{streamBody: streamOf(long)}
	out := &lineWriter{}
	if err := runBridgeCmd(t, f.deps(nil), out, "--agent", "leo-alpha"); err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if len(out.writes) != 1 || out.writes[0] != long {
		t.Fatalf("a %d-byte command must pass through as one line, got %d writes", len(long), len(out.writes))
	}
}

func TestBridgeStreamAgentDefaultsToBridgeAgent(t *testing.T) {
	f := &fakeBridge{streamBody: streamOf("")}
	env := map[string]string{"LEO_BRIDGE_AGENT": "dispatch.d-0123456789ab", "LEO_PROCESS_NAME": "leo-display"}
	if err := runBridgeCmd(t, f.deps(env), io.Discard); err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if f.streamAgent != "dispatch.d-0123456789ab" {
		t.Fatalf("agent=%q, want LEO_BRIDGE_AGENT", f.streamAgent)
	}
	f = &fakeBridge{streamBody: streamOf("")}
	if err := runBridgeCmd(t, f.deps(env), io.Discard, "--agent", "leo-flag"); err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if f.streamAgent != "leo-flag" {
		t.Fatalf("agent=%q, --agent must win over the environment", f.streamAgent)
	}
}

func TestBridgeStreamRequiresValidAgent(t *testing.T) {
	// LEO_PROCESS_NAME is a display name, never a bridge key.
	env := map[string]string{"LEO_PROCESS_NAME": "leo-display"}
	for _, args := range [][]string{{}, {"--agent", "bad name"}, {"--agent", "../x"}} {
		f := &fakeBridge{streamBody: streamOf("")}
		if err := runBridgeCmd(t, f.deps(env), io.Discard, args...); err == nil {
			t.Fatalf("args %q: want an error", args)
		}
		if f.streamAgent != "" {
			t.Fatalf("args %q: connected despite an invalid agent", args)
		}
	}
}

func TestBridgeStreamConnectFailureExitsNonZero(t *testing.T) {
	f := &fakeBridge{streamErr: errors.New("connecting to daemon: refused")}
	err := runBridgeCmd(t, f.deps(nil), io.Discard, "--agent", "leo-alpha")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err=%v, want the connect failure", err)
	}
}

// A stream cut mid-command must never hand the mod half a JSON line.
func TestBridgeStreamDropsTruncatedCommand(t *testing.T) {
	f := &fakeBridge{streamBody: streamOf("{\"id\":\"c1\"}\n{\"id\":\"c2\"")}
	var out bytes.Buffer
	if err := runBridgeCmd(t, f.deps(nil), &out, "--agent", "leo-alpha"); err == nil {
		t.Fatal("a truncated stream must exit non-zero")
	}
	if out.String() != "{\"id\":\"c1\"}\n" {
		t.Fatalf("stdout=%q, want only the complete line", out.String())
	}
}

type failingReader struct{ data io.Reader }

func (r failingReader) Read(p []byte) (int, error) {
	n, err := r.data.Read(p)
	if errors.Is(err, io.EOF) {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestBridgeStreamReadErrorExitsNonZero(t *testing.T) {
	f := &fakeBridge{streamBody: io.NopCloser(failingReader{strings.NewReader("{\"id\":\"c1\"}\n")})}
	var out bytes.Buffer
	if err := runBridgeCmd(t, f.deps(nil), &out, "--agent", "leo-alpha"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want the read failure", err)
	}
	if out.String() != "{\"id\":\"c1\"}\n" {
		t.Fatalf("stdout=%q, lines read before the failure must still pass through", out.String())
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestBridgeStreamStdoutFailureExitsNonZero(t *testing.T) {
	f := &fakeBridge{streamBody: streamOf("{\"id\":\"c1\"}\n")}
	if err := runBridgeCmd(t, f.deps(nil), errWriter{}, "--agent", "leo-alpha"); err == nil {
		t.Fatal("a dead stdout must exit non-zero")
	}
}

// An orphaned bridge would keep the agent's stream registered after claude
// is gone; it must hang up once its parent disappears.
func TestBridgeStreamHangsUpWhenParentGone(t *testing.T) {
	body, feed := io.Pipe()
	defer func() { _ = feed.Close() }()
	f := &fakeBridge{streamBody: body}
	deps := f.deps(nil)
	done := make(chan error, 1)
	go func() { done <- runBridgeCmd(t, deps, io.Discard, "--agent", "leo-alpha") }()
	close(f.parentGone)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("hanging up on a vanished parent must exit 0, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge kept the stream open after its parent went away")
	}
}

func TestBridgeReportPostsStdinVerbatim(t *testing.T) {
	f := &fakeBridge{}
	body := `{"type":"event","name":"turn.complete","message":"line 1\nline 2"}`
	err := runBridgeCmdWithStdin(t, f.deps(nil), strings.NewReader(body), io.Discard, "report", "--agent", "leo-alpha")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if string(f.reportBody) != body || f.reportAgent != "leo-alpha" || f.reportHome != "/leo-home" {
		t.Fatalf("posted %q for %q in %q", f.reportBody, f.reportAgent, f.reportHome)
	}
}

// A report can carry a whole prompt or final message, well past what argv
// can hold; stdin takes it whole.
func TestBridgeReportPostsLargeStdin(t *testing.T) {
	f := &fakeBridge{}
	body := `{"type":"event","name":"turn.start","prompt":"` + strings.Repeat("p", 2<<20) + `"}`
	err := runBridgeCmdWithStdin(t, f.deps(nil), strings.NewReader(body), io.Discard, "report", "--agent", "leo-alpha")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(f.reportBody) != len(body) {
		t.Fatalf("posted %d bytes, want %d", len(f.reportBody), len(body))
	}
}

func TestBridgeReportAgentDefaultsToBridgeAgent(t *testing.T) {
	f := &fakeBridge{}
	env := map[string]string{"LEO_BRIDGE_AGENT": "leo-from-env", "LEO_PROCESS_NAME": "leo-display"}
	err := runBridgeCmdWithStdin(t, f.deps(env), strings.NewReader(`{"type":"event","name":"turn.start"}`), io.Discard, "report")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if f.reportAgent != "leo-from-env" {
		t.Fatalf("agent=%q, want LEO_BRIDGE_AGENT", f.reportAgent)
	}
}

func TestBridgeReportFailureExitsNonZero(t *testing.T) {
	f := &fakeBridge{reportErr: errors.New("bridge report: daemon returned 400: unknown type")}
	err := runBridgeCmdWithStdin(t, f.deps(nil), strings.NewReader(`{"type":"bogus"}`), io.Discard, "report", "--agent", "leo-alpha")
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err=%v, want the daemon's rejection", err)
	}
}

// The daemon forgot the key: the launch this claude belongs to is over, so
// the report is moot. Exiting 0 keeps the mod from retrying it for a minute.
func TestBridgeReportToAGoneKeySucceeds(t *testing.T) {
	f := &fakeBridge{reportErr: fmt.Errorf("%w: bridge report: daemon returned 410: forgotten", daemon.ErrBridgeGone)}
	err := runBridgeCmdWithStdin(t, f.deps(nil), strings.NewReader(`{"type":"event","name":"turn.start"}`), io.Discard, "report", "--agent", "leo-alpha")
	if err != nil {
		t.Fatalf("report to a gone key: err=%v, want nil", err)
	}
	if f.reports != 1 {
		t.Fatalf("reports=%d, want 1", f.reports)
	}
}

func TestBridgeReportInputValidation(t *testing.T) {
	report := `{"type":"ack","id":"c1","ok":true}`
	cases := []struct {
		name  string
		env   map[string]string
		stdin string
		args  []string
	}{
		{"empty stdin", nil, "", []string{"report", "--agent", "leo-alpha"}},
		{"blank stdin", nil, " \n\t", []string{"report", "--agent", "leo-alpha"}},
		{"payload as an argument", nil, "", []string{"report", "--agent", "leo-alpha", report}},
		{"no agent", map[string]string{"LEO_PROCESS_NAME": "leo-display"}, report, []string{"report"}},
		{"invalid agent", nil, report, []string{"report", "--agent", "bad name"}},
		{"oversized stdin", nil, strings.Repeat("x", int(daemon.MaxBridgeReportBytes)+1), []string{"report", "--agent", "leo-alpha"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBridge{}
			if err := runBridgeCmdWithStdin(t, f.deps(tc.env), strings.NewReader(tc.stdin), io.Discard, tc.args...); err == nil {
				t.Fatal("want an error")
			}
			if f.reports != 0 {
				t.Fatal("posted despite invalid input")
			}
		})
	}
}

func TestBridgeHomeDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cfg := filepath.Join(t.TempDir(), "leo.yaml")
	if got := bridgeHomeDir(cfg, env(map[string]string{"LEO_HOME": "/elsewhere"})); got != filepath.Dir(cfg) {
		t.Fatalf("--config: got %q, want %q", got, filepath.Dir(cfg))
	}
	if got := bridgeHomeDir("", env(map[string]string{"LEO_HOME": "/elsewhere"})); got != "/elsewhere" {
		t.Fatalf("LEO_HOME: got %q", got)
	}
	// The launching daemon names its home in the bridged claude's env; it
	// wins over LEO_HOME, which may name some other daemon.
	if got := bridgeHomeDir("", env(map[string]string{"LEO_BRIDGE_HOME": "/launcher", "LEO_HOME": "/elsewhere"})); got != "/launcher" {
		t.Fatalf("LEO_BRIDGE_HOME: got %q", got)
	}
	if got := bridgeHomeDir(cfg, env(map[string]string{"LEO_BRIDGE_HOME": "/launcher"})); got != filepath.Dir(cfg) {
		t.Fatalf("--config must still win: got %q", got)
	}
	if got := bridgeHomeDir("", env(nil)); got == "" {
		t.Fatal("default home must not be empty")
	}
}

func TestBridgeCommandIsHiddenAndRegistered(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"bridge"})
	if err != nil || cmd.Name() != "bridge" {
		t.Fatalf("bridge not registered: %v", err)
	}
	if !cmd.Hidden {
		t.Fatal("bridge is internal plumbing and must be hidden from help")
	}
	report, _, err := root.Find([]string{"bridge", "report"})
	if err != nil || report.Name() != "report" {
		t.Fatalf("bridge report not registered: %v", err)
	}
}
