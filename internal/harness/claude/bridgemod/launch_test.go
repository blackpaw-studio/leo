package bridgemod

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{in: "2.1.289 (Claude Code)", want: Version{2, 1, 289}},
		{in: "2.1.287", want: Version{2, 1, 287}},
		{in: "  2.1.290 (Claude Code)\n", want: Version{2, 1, 290}},
		{in: "2.2.0-beta.1 (Claude Code)", want: Version{2, 2, 0}},
		{in: "10.0.1", want: Version{10, 0, 1}},
		{in: "", wantErr: true},
		{in: "Claude Code", wantErr: true},
		{in: "2.1", wantErr: true},
		{in: "2.x.3", wantErr: true},
		{in: "-1.2.3", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseVersion(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseVersion(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestVersionGates(t *testing.T) {
	tests := []struct {
		v               Version
		supports, newer bool
	}{
		{Version{2, 1, 286}, false, false},
		{Version{2, 1, 287}, true, false},
		{Version{2, 1, 289}, true, false},
		{Version{2, 1, 290}, true, true},
		{Version{2, 2, 0}, true, true},
		{Version{1, 9, 999}, false, false},
		{Version{3, 0, 0}, true, true},
	}
	for _, tt := range tests {
		if got := SupportsMods(tt.v); got != tt.supports {
			t.Errorf("SupportsMods(%v) = %v, want %v", tt.v, got, tt.supports)
		}
		if got := NewerThanTested(tt.v); got != tt.newer {
			t.Errorf("NewerThanTested(%v) = %v, want %v", tt.v, got, tt.newer)
		}
	}
	if MinClaudeVersion != "2.1.287" || TestedClaudeVersion != "2.1.289" {
		t.Fatalf("version constants moved: min %s tested %s", MinClaudeVersion, TestedClaudeVersion)
	}
}

// countingProbe answers `claude --version` per binary path and counts calls.
type countingProbe struct {
	mu      sync.Mutex
	answers map[string]string
	errs    map[string]error
	calls   map[string]int
}

func (p *countingProbe) probe(_ context.Context, claudePath string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[claudePath]++
	if err := p.errs[claudePath]; err != nil {
		return "", err
	}
	return p.answers[claudePath], nil
}

func (p *countingProbe) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[path]
}

func newTestLauncher(t *testing.T, probe VersionProbe, log *bytes.Buffer) *Launcher {
	t.Helper()
	return NewLauncher(LauncherOptions{
		StateDir:   t.TempDir(),
		LeoVersion: "v0.31.0-test",
		LeoBin:     "/opt/leo/bin/leo",
		LeoHome:    "/srv/leo-home",
		Probe:      probe,
		Log:        log,
	})
}

func TestPlanForACapableClaude(t *testing.T) {
	p := &countingProbe{answers: map[string]string{"/bin/claude": "2.1.289 (Claude Code)"}}
	l := newTestLauncher(t, p.probe, &bytes.Buffer{})

	plan, ok := l.Plan(context.Background(), "/bin/claude", "leo-alpha")
	if !ok {
		t.Fatal("Plan refused a 2.1.289 claude")
	}
	if plan.Key != "leo-alpha" {
		t.Fatalf("Key = %q", plan.Key)
	}
	if _, err := os.Stat(filepath.Join(plan.PluginDir, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("PluginDir %q holds no materialized mod: %v", plan.PluginDir, err)
	}
	if !strings.Contains(plan.PluginDir, "v0.31.0-test-") {
		t.Fatalf("PluginDir %q is not versioned by the leo version", plan.PluginDir)
	}
	want := map[string]string{EnvBin: "/opt/leo/bin/leo", EnvAgent: "leo-alpha", EnvHome: "/srv/leo-home", EnvLaunch: plan.Env[EnvLaunch]}
	if len(plan.Env) != len(want) || plan.Env[EnvBin] != want[EnvBin] || plan.Env[EnvAgent] != want[EnvAgent] || plan.Env[EnvHome] != want[EnvHome] || plan.Env[EnvLaunch] == "" {
		t.Fatalf("Env = %v, want %v and a launch id", plan.Env, want)
	}
	for _, k := range EnvKeys {
		if _, ok := plan.Env[k]; !ok {
			t.Errorf("EnvKeys lists %s, which a bridged launch does not set", k)
		}
	}
	if len(EnvKeys) != len(plan.Env) {
		t.Errorf("EnvKeys = %v; a bridged launch sets %v", EnvKeys, plan.Env)
	}
	args := plan.Args([]string{"--session-id", "s1", "--name", "alpha"})
	wantArgs := []string{"--plugin-dir", plan.PluginDir, "--session-id", "s1", "--name", "alpha"}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("Args = %q, want %q", args, wantArgs)
	}
}

func TestPlanRefusesAnOldClaude(t *testing.T) {
	p := &countingProbe{answers: map[string]string{"/bin/claude": "2.1.286 (Claude Code)"}}
	var log bytes.Buffer
	l := newTestLauncher(t, p.probe, &log)
	if _, ok := l.Plan(context.Background(), "/bin/claude", "leo-alpha"); ok {
		t.Fatal("Plan accepted a claude older than the mods API")
	}
	if l.Capable(context.Background(), "/bin/claude") {
		t.Fatal("Capable = true for 2.1.286")
	}
	if !strings.Contains(log.String(), "2.1.286") {
		t.Fatalf("no log line naming the old version: %q", log.String())
	}
}

func TestPlanRejectsBadKeys(t *testing.T) {
	p := &countingProbe{answers: map[string]string{"/bin/claude": "2.1.289"}}
	l := newTestLauncher(t, p.probe, &bytes.Buffer{})
	for _, key := range []string{"", "bad name", "../x", "a/b"} {
		if _, ok := l.Plan(context.Background(), "/bin/claude", key); ok {
			t.Errorf("Plan accepted key %q", key)
		}
	}
}

// The version is probed once per claude binary. Claude updates itself by
// repointing a symlink at a new versioned file, so the cache is keyed by the
// resolved binary: an update is noticed without re-probing on every launch.
func TestVersionIsProbedOncePerResolvedBinary(t *testing.T) {
	dir := t.TempDir()
	v1 := filepath.Join(dir, "2.1.289")
	v2 := filepath.Join(dir, "2.1.290")
	for _, f := range []string{v1, v2} {
		if err := os.WriteFile(f, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "claude")
	if err := os.Symlink(v1, link); err != nil {
		t.Fatal(err)
	}
	p := &countingProbe{answers: map[string]string{link: "2.1.289 (Claude Code)"}}
	l := newTestLauncher(t, p.probe, &bytes.Buffer{})
	ctx := context.Background()
	for range 3 {
		if !l.Capable(ctx, link) {
			t.Fatal("Capable = false")
		}
	}
	if n := p.count(link); n != 1 {
		t.Fatalf("probed %d times for one binary, want 1", n)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v2, link); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.answers[link] = "2.1.290 (Claude Code)"
	p.mu.Unlock()
	v, err := l.ClaudeVersion(ctx, link)
	if err != nil || v != (Version{2, 1, 290}) {
		t.Fatalf("after the update: %v, %v; want 2.1.290", v, err)
	}
	if n := p.count(link); n != 2 {
		t.Fatalf("probed %d times across an update, want 2", n)
	}
}

// A failed probe is not cached for good: claude may be installed or fixed
// while the daemon runs. It is retried after a while, not on every launch.
func TestFailedProbeIsRetriedLater(t *testing.T) {
	p := &countingProbe{errs: map[string]error{"/bin/claude": errors.New("exec: not found")}}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	l := NewLauncher(LauncherOptions{
		StateDir: t.TempDir(), LeoVersion: "v1", LeoBin: "/opt/leo/bin/leo",
		Probe: p.probe, Log: &bytes.Buffer{}, Now: func() time.Time { return now },
	})
	ctx := context.Background()
	for range 2 {
		if l.Capable(ctx, "/bin/claude") {
			t.Fatal("Capable = true with a failing probe")
		}
	}
	if n := p.count("/bin/claude"); n != 1 {
		t.Fatalf("probed %d times within the retry window, want 1", n)
	}
	now = now.Add(probeRetryAfter + time.Second)
	p.mu.Lock()
	p.errs = nil
	p.answers = map[string]string{"/bin/claude": "2.1.289"}
	p.mu.Unlock()
	if !l.Capable(ctx, "/bin/claude") {
		t.Fatal("Capable = false after the probe recovered")
	}
}

func TestNilLauncherIsLegacy(t *testing.T) {
	var l *Launcher
	if l.Capable(context.Background(), "/bin/claude") {
		t.Fatal("nil Launcher Capable")
	}
	if _, ok := l.Plan(context.Background(), "/bin/claude", "leo-alpha"); ok {
		t.Fatal("nil Launcher planned a bridged launch")
	}
}

// A mod that cannot be written means a legacy launch, not a failed one.
func TestPlanFallsBackWhenTheModCannotBeWritten(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(stateFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := &countingProbe{answers: map[string]string{"/bin/claude": "2.1.289"}}
	var log bytes.Buffer
	l := NewLauncher(LauncherOptions{StateDir: stateFile, LeoVersion: "v1", LeoBin: "/opt/leo/bin/leo", Probe: p.probe, Log: &log})
	if _, ok := l.Plan(context.Background(), "/bin/claude", "leo-alpha"); ok {
		t.Fatal("Plan succeeded without a materialized mod")
	}
	if !strings.Contains(log.String(), "bridge") {
		t.Fatalf("no log line for the failed materialize: %q", log.String())
	}
}

// The real probe runs `<claude> --version`.
func TestProbeClaudeVersionRunsTheBinary(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '2.1.289 (Claude Code)'; exit 0; fi\nexit 3\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ProbeClaudeVersion(context.Background(), fake)
	if err != nil || strings.TrimSpace(got) != "2.1.289 (Claude Code)" {
		t.Fatalf("ProbeClaudeVersion = %q, %v", got, err)
	}
	if _, err := ProbeClaudeVersion(context.Background(), filepath.Join(dir, "missing")); err == nil {
		t.Fatal("ProbeClaudeVersion of a missing binary succeeded")
	}
}

// gatedProbe blocks every probe until release is closed, honouring the
// probe's ctx as the real one does (exec.CommandContext kills the child).
type gatedProbe struct {
	countingProbe
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedProbe(answers map[string]string) *gatedProbe {
	return &gatedProbe{
		countingProbe: countingProbe{answers: answers},
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
}

func (p *gatedProbe) probe(ctx context.Context, claudePath string) (string, error) {
	p.mu.Lock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[claudePath]++
	p.mu.Unlock()
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answers[claudePath], nil
}

// Launches that need the same claude's version at once share one probe
// rather than each running `claude --version`.
func TestConcurrentProbesOfOneBinaryShareOneRun(t *testing.T) {
	p := newGatedProbe(map[string]string{"/bin/claude": "2.1.289 (Claude Code)"})
	l := newTestLauncher(t, p.probe, &bytes.Buffer{})
	const callers = 4
	type result struct {
		v   Version
		err error
	}
	results := make(chan result, callers)
	for range callers {
		go func() {
			v, err := l.ClaudeVersion(context.Background(), "/bin/claude")
			results <- result{v, err}
		}()
	}
	<-p.started
	// Give every caller the chance to start a probe of its own.
	time.Sleep(50 * time.Millisecond)
	close(p.release)
	for range callers {
		r := <-results
		if r.err != nil || r.v != (Version{2, 1, 289}) {
			t.Fatalf("ClaudeVersion = %v, %v", r.v, r.err)
		}
	}
	if n := p.count("/bin/claude"); n != 1 {
		t.Fatalf("probed %d times for concurrent callers, want 1", n)
	}
}

// A caller that gives up on the probe (its launch was canceled) must not
// leave a failure behind: the probe finishes for whoever asks next, and its
// success is what is cached.
func TestACanceledCallerDoesNotCacheAFailedProbe(t *testing.T) {
	p := newGatedProbe(map[string]string{"/bin/claude": "2.1.289 (Claude Code)"})
	l := newTestLauncher(t, p.probe, &bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := l.ClaudeVersion(ctx, "/bin/claude")
		done <- err
	}()
	<-p.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller got %v, want context.Canceled", err)
	}
	close(p.release)
	v, err := l.ClaudeVersion(context.Background(), "/bin/claude")
	if err != nil || v != (Version{2, 1, 289}) {
		t.Fatalf("after a canceled caller: %v, %v; want 2.1.289", v, err)
	}
	if n := p.count("/bin/claude"); n != 1 {
		t.Fatalf("probed %d times, want the one probe to have served both callers", n)
	}
}

// A failed probe never replaces a success: whichever finished last, the
// binary is known to answer.
func TestAProbeFailureNeverReplacesASuccess(t *testing.T) {
	l := newTestLauncher(t, (&countingProbe{}).probe, &bytes.Buffer{})
	now := time.Now()
	l.record("/bin/claude", probeResult{version: Version{2, 1, 289}, at: now})
	l.record("/bin/claude", probeResult{err: errors.New("killed"), at: now.Add(time.Second)})
	v, err := l.ClaudeVersion(context.Background(), "/bin/claude")
	if err != nil || v != (Version{2, 1, 289}) {
		t.Fatalf("ClaudeVersion = %v, %v; want the recorded success", v, err)
	}
}

// A bare command name (dispatches launch "claude" from PATH) is resolved
// through PATH before its symlinks, so an update is noticed there too.
func TestBareClaudeIsResolvedThroughPATH(t *testing.T) {
	dir := t.TempDir()
	v1, v2 := filepath.Join(dir, "2.1.289"), filepath.Join(dir, "2.1.290")
	for _, f := range []string{v1, v2} {
		if err := os.WriteFile(f, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "claude")
	if err := os.Symlink(v1, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	p := &countingProbe{answers: map[string]string{"claude": "2.1.289 (Claude Code)"}}
	l := newTestLauncher(t, p.probe, &bytes.Buffer{})
	ctx := context.Background()
	if _, err := l.ClaudeVersion(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v2, link); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.answers["claude"] = "2.1.290 (Claude Code)"
	p.mu.Unlock()
	v, err := l.ClaudeVersion(ctx, "claude")
	if err != nil || v != (Version{2, 1, 290}) {
		t.Fatalf("after the update: %v, %v; want 2.1.290", v, err)
	}
}

// Each launch is named afresh: the mod tells a hot reload of itself (same
// launch: the engine still holds what it handed over) from a new process
// (whose prompt queue starts empty) by it.
func TestEachPlanNamesItsLaunch(t *testing.T) {
	p := &countingProbe{answers: map[string]string{"/bin/claude": "2.1.289"}}
	l := newTestLauncher(t, p.probe, &bytes.Buffer{})
	first, ok := l.Plan(context.Background(), "/bin/claude", "leo-alpha")
	if !ok {
		t.Fatal("Plan refused")
	}
	second, ok := l.Plan(context.Background(), "/bin/claude", "leo-alpha")
	if !ok {
		t.Fatal("Plan refused")
	}
	a, b := first.Env[EnvLaunch], second.Env[EnvLaunch]
	if a == "" || b == "" || a == b {
		t.Fatalf("launch ids %q and %q: want two distinct ids", a, b)
	}
	if first.Launch != a || second.Launch != b {
		t.Fatalf("Plan.Launch = %q, %q; want the env's %q, %q", first.Launch, second.Launch, a, b)
	}
	if !slices.Contains(EnvKeys, EnvLaunch) {
		t.Fatalf("EnvKeys = %v lacks %s, so other launches would not blank it", EnvKeys, EnvLaunch)
	}
}
