package bridgemod

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	want := map[string]string{EnvBin: "/opt/leo/bin/leo", EnvAgent: "leo-alpha"}
	if len(plan.Env) != len(want) || plan.Env[EnvBin] != want[EnvBin] || plan.Env[EnvAgent] != want[EnvAgent] {
		t.Fatalf("Env = %v, want %v", plan.Env, want)
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
