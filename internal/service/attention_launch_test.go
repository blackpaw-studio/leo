package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// fakeAttentionDriver adds harness.AttentionHooker to fakeHookDriver.
type fakeAttentionDriver struct {
	fakeHookDriver
	supported bool
}

func (d *fakeAttentionDriver) AttentionLaunch(_ harness.SessionHandle, args, reportCmd []string) ([]string, bool, error) {
	if !d.supported {
		return args, false, nil
	}
	return append(append([]string(nil), args...), "--hooked", strings.Join(reportCmd, " ")), true, nil
}

func (d *fakeAttentionDriver) AttentionSupported() bool { return d.supported }

// liveTmuxStub launches successfully and keeps the session alive, logging
// every invocation. has-session reports alive (so adoption applies).
func liveTmuxStub(t *testing.T) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	path, logPath = filepath.Join(dir, "tmux"), filepath.Join(dir, "tmux.log")
	// On new-session it snapshots $LEO_TEST_SNAP_SRC (when set) to
	// $LEO_TEST_SNAP_DST, capturing the store as the session is created.
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nfor a in \"$@\"; do case \"$a\" in new-session) [ -n \"$LEO_TEST_SNAP_SRC\" ] && cp \"$LEO_TEST_SNAP_SRC\" \"$LEO_TEST_SNAP_DST\"; echo '%1'; exit 0;; display-message) echo 0; exit 0;; esac; done\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}
	return path, logPath
}

type launchFixture struct {
	sv      *Supervisor
	store   *observe.AttentionStore
	logPath string
}

func newLaunchFixture(t *testing.T, drv harness.SessionDriver, rec agentstore.Record) launchFixture {
	t.Helper()
	testFakeDriver = drv
	t.Cleanup(func() { testFakeDriver = nil })
	origCmd := attentionReportCmd
	attentionReportCmd = func() []string { return []string{"/opt/leo", "dispatch", "report"} }
	t.Cleanup(func() { attentionReportCmd = origCmd })
	origPoll := sessionPollInterval
	sessionPollInterval = time.Hour
	t.Cleanup(func() { sessionPollInterval = origPoll })

	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	tmuxPath, logPath := liveTmuxStub(t)
	sv.tmuxPath = tmuxPath
	sv.homePath = t.TempDir()
	store := observe.NewAttentionStore(nil)
	sv.SetAttention(store)
	if rec.Name != "" {
		if err := agentstore.Save(sv.homePath, rec); err != nil {
			t.Fatal(err)
		}
		if err := agentstore.SetAttentionToken(sv.homePath, rec.Name, rec.AttentionToken); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = sv.StopAgent(rec.Name, false)
		cancel()
	})
	return launchFixture{sv: sv, store: store, logPath: logPath}
}

func (f launchFixture) spawn(t *testing.T, spec daemon.AgentSpawnSpec) {
	t.Helper()
	spec.Harness = "fakehook"
	if spec.WorkDir == "" {
		spec.WorkDir = t.TempDir()
	}
	if err := f.sv.SpawnAgent(spec); err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}
}

// storedToken waits for name's record to carry a non-empty attention token.
func storedToken(t *testing.T, home, name string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs, _ := agentstore.Load(agentstore.FilePath(home))
		if tok := recs[name].AttentionToken; tok != "" || time.Now().After(deadline) {
			return tok
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// launchedTokens returns every LEO_ATTENTION_TOKEN a new-session carried.
func launchedTokens(t *testing.T, logPath string) []string {
	t.Helper()
	logged, _ := os.ReadFile(logPath)
	var out []string
	for _, m := range regexp.MustCompile(`LEO_ATTENTION_TOKEN=([0-9a-f]+)`).FindAllStringSubmatch(string(logged), -1) {
		out = append(out, m[1])
	}
	return out
}

func TestFreshSpawnInjectsAttentionHooksWithoutPersistingArgs(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "hooked"})

	f.spawn(t, daemon.AgentSpawnSpec{Name: "hooked", ClaudeArgs: []string{"--base"}})

	logged := waitForLog(t, f.logPath, "new-session")
	if !strings.Contains(logged, "'--base' '--hooked' '/opt/leo dispatch report'") {
		t.Fatalf("spawned command lacks hooks:\n%s", logged)
	}
	waitAttention(t, f.store, "hooked", observe.AttentionUnknown)
	tok := storedToken(t, f.sv.homePath, "hooked")
	if launched := launchedTokens(t, f.logPath); len(tok) < 32 || !reflect.DeepEqual(launched, []string{tok}) {
		t.Fatalf("stored token %q, launched with %v; want one matching random token", tok, launched)
	}
	if name, ok := f.store.AgentForToken(tok); !ok || name != "hooked" {
		t.Fatalf("token routes to %q, %v; want hooked", name, ok)
	}
	f.sv.mu.RLock()
	stored := f.sv.identities["hooked"].Args()
	f.sv.mu.RUnlock()
	if !reflect.DeepEqual(stored, []string{"--base"}) {
		t.Fatalf("stored args = %#v, want hooks kept out", stored)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(f.sv.homePath))
	if len(recs["hooked"].ClaudeArgs) != 0 {
		t.Fatalf("record args = %#v, want untouched", recs["hooked"].ClaudeArgs)
	}
}

// A resumed launch knows nothing about the pane yet: only hooks set
// working, so a restarted-but-idle agent never reads as working.
func TestResumedSpawnWithHooksIsUnknown(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "resumed"})
	pub := &recordingPublisher{}
	f.sv.SetAttention(observe.NewAttentionStore(pub))
	f.store = f.sv.attentionStore()

	f.spawn(t, daemon.AgentSpawnSpec{Name: "resumed", Resumed: true})
	waitForLog(t, f.logPath, "new-session")

	// Wait for the launch's own attention write (spawn preset + launch).
	deadline := time.Now().Add(5 * time.Second)
	for len(pub.Events()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for _, ev := range pub.Events() {
		if att := ev.Payload.(*observe.AgentActivityPayload).Attention; att == nil || att.State != observe.AttentionUnknown {
			t.Fatalf("resumed launch published attention %+v, want only unknown", att)
		}
	}
	waitAttention(t, f.store, "resumed", observe.AttentionUnknown)
}

// The spawn window: a hooked agent carries attention the moment SpawnAgent
// returns (before its launch goroutine runs) and on agent_spawned; an
// unhooked one carries neither.
func TestSpawnCarriesAttentionBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supported bool
	}{{"hooked", true}, {"unhooked", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t, &fakeAttentionDriver{supported: tc.supported}, agentstore.Record{Name: "fresh"})
			pub := &recordingPublisher{}
			f.sv.SetPublisher(pub)

			f.spawn(t, daemon.AgentSpawnSpec{Name: "fresh"})

			att, present := f.store.Get("fresh")
			if present != tc.supported || (present && att.State != observe.AttentionUnknown) {
				t.Fatalf("attention right after SpawnAgent = %+v (present=%v), want unknown only when hooked", att, present)
			}
			var spawned *observe.AgentSpawnedPayload
			for _, ev := range pub.Events() {
				if p, ok := ev.Payload.(*observe.AgentSpawnedPayload); ok {
					spawned = p
				}
			}
			if spawned == nil {
				t.Fatal("no agent_spawned published")
			}
			got := spawned.Agent.Attention
			if (got != nil) != tc.supported || (got != nil && got.State != observe.AttentionUnknown) {
				t.Fatalf("agent_spawned attention = %+v, want unknown only when hooked", got)
			}
		})
	}
}

func TestSpawnWithoutAttentionSupportLeavesAttentionAbsent(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: false}, agentstore.Record{Name: "plain", AttentionToken: "stale"})
	f.store.Set("plain", observe.AttentionFinished) // stale entry from a hooked past

	f.spawn(t, daemon.AgentSpawnSpec{Name: "plain", ClaudeArgs: []string{"--base"}})

	logged := waitForLog(t, f.logPath, "new-session")
	if strings.Contains(logged, "--hooked") || strings.Contains(logged, "LEO_ATTENTION_TOKEN") {
		t.Fatalf("unsupported driver got hooks:\n%s", logged)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, present := f.store.Get("plain")
		recs, _ := agentstore.Load(agentstore.FilePath(f.sv.homePath))
		if !present && recs["plain"].AttentionToken == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("present=%v token=%q; want attention absent and token cleared", present, recs["plain"].AttentionToken)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAdoptSetsUnknownOnlyForHookedRecords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		token   string
		present bool
	}{{"hooked", "persisted-tok", true}, {"unhooked", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "adopted", AttentionToken: tc.token})

			f.spawn(t, daemon.AgentSpawnSpec{Name: "adopted", Adopt: true})
			waitForLog(t, f.logPath, "show-options")

			if tc.present {
				waitAttention(t, f.store, "adopted", observe.AttentionUnknown)
				// The surviving process still reports with its launch token.
				deadline := time.Now().Add(5 * time.Second)
				for {
					name, ok := f.store.AgentForToken(tc.token)
					if ok && name == "adopted" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("persisted token routes to %q, %v; want adopted", name, ok)
					}
					time.Sleep(5 * time.Millisecond)
				}
			} else {
				time.Sleep(50 * time.Millisecond)
				if att, ok := f.store.Get("adopted"); ok {
					t.Fatalf("attention = %+v, want absent", att)
				}
			}
			if logged, _ := os.ReadFile(f.logPath); strings.Contains(string(logged), "new-session") {
				t.Fatalf("adopt path spawned a new session:\n%s", logged)
			}
		})
	}
}

func TestHookAfterStopIsNoop(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "stopped"})
	f.spawn(t, daemon.AgentSpawnSpec{Name: "stopped"})
	waitAttention(t, f.store, "stopped", observe.AttentionUnknown)
	tok := storedToken(t, f.sv.homePath, "stopped")
	f.store.SetByToken(tok, observe.AttentionWorking)
	// A token StopAgent itself must drop: the supervise goroutine's own
	// post-exit unregister may still be in flight when StopAgent returns.
	f.store.RegisterToken("in-flight", "stopped")

	if err := f.sv.StopAgent("stopped", false); err != nil {
		t.Fatal(err)
	}
	after, _ := f.store.Get("stopped")

	for _, late := range []string{tok, "in-flight"} {
		if att, ok := f.store.SetByToken(late, observe.AttentionFinished); ok {
			t.Fatalf("late hook (%s) after stop applied: %+v", late, att)
		}
	}
	if got, _ := f.store.Get("stopped"); got != after || got.State != observe.AttentionUnknown {
		t.Fatalf("attention = %+v, want unknown %+v untouched", got, after)
	}
}

func TestClaudeAgentLaunchCarriesOneMergedSettings(t *testing.T) {
	f := newLaunchFixture(t, nil, agentstore.Record{Name: "claude-agent"})
	if err := f.sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name:       "claude-agent",
		Harness:    "claude",
		WorkDir:    t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet", "--settings", `{"crossSessionInbound":"accept"}`},
	}); err != nil {
		t.Fatal(err)
	}

	logged := waitForLog(t, f.logPath, "new-session")

	hook := `[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}]}]`
	wantSettings := `'--settings' '{"crossSessionInbound":"accept","hooks":{` +
		`"Notification":[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}],"matcher":"permission_prompt|elicitation_dialog"}],` +
		`"PostToolUse":` + hook + `,"SessionEnd":` + hook + `,"Stop":` + hook + `,"UserPromptSubmit":` + hook + `}}'`
	if !strings.Contains(logged, "'--model' 'sonnet' "+wantSettings) {
		t.Fatalf("spawned command lacks the exact merged settings:\n%s\nwant substring:\n%s", logged, wantSettings)
	}
	if n := strings.Count(logged, "--settings"); n != 1 {
		t.Fatalf("--settings appears %d times, want 1:\n%s", n, logged)
	}
}

// The launch token must be on disk before the session exists, so an adopt
// after a daemon crash mid-launch re-registers the live launch's token and
// never the previous one's.
func TestLaunchTokenPersistedBeforeSessionCreated(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "early", AttentionToken: "previous-launch"})
	snap := filepath.Join(t.TempDir(), "agents-at-new-session.json")
	t.Setenv("LEO_TEST_SNAP_SRC", agentstore.FilePath(f.sv.homePath))
	t.Setenv("LEO_TEST_SNAP_DST", snap)

	f.spawn(t, daemon.AgentSpawnSpec{Name: "early"})
	waitForLog(t, f.logPath, "new-session")

	launched := launchedTokens(t, f.logPath)
	atCreate, err := agentstore.Load(snap)
	if err != nil || len(launched) != 1 || atCreate["early"].AttentionToken != launched[0] {
		t.Fatalf("token on disk at new-session = %q (err %v), launched with %v", atCreate["early"].AttentionToken, err, launched)
	}
	if _, ok := f.store.AgentForToken("previous-launch"); ok {
		t.Fatal("previous launch's token was registered")
	}
}

// With nowhere to persist the token (no agent record), the launch goes
// unhooked: a token adopt could never recover must not be handed out.
func TestFailedTokenPersistLaunchesUnhooked(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{})

	f.spawn(t, daemon.AgentSpawnSpec{Name: "recordless", ClaudeArgs: []string{"--base"}})

	logged := waitForLog(t, f.logPath, "new-session")
	if strings.Contains(logged, "--hooked") || strings.Contains(logged, "LEO_ATTENTION_TOKEN") {
		t.Fatalf("launch hooked despite the failed persist:\n%s", logged)
	}
	time.Sleep(50 * time.Millisecond)
	if att, ok := f.store.Get("recordless"); ok {
		t.Fatalf("attention = %+v, want absent", att)
	}
	_ = f.sv.StopAgent("recordless", false)
}

func TestStopClearsStoredToken(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "stopme"})
	f.spawn(t, daemon.AgentSpawnSpec{Name: "stopme"})
	if storedToken(t, f.sv.homePath, "stopme") == "" {
		t.Fatal("no token stored")
	}

	if err := f.sv.StopAgent("stopme", false); err != nil {
		t.Fatal(err)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(f.sv.homePath))
	if tok := recs["stopme"].AttentionToken; tok != "" {
		t.Fatalf("stored token after stop = %q, want cleared", tok)
	}
}
