package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

// liveTmuxStub launches successfully and keeps the session alive, logging
// every invocation. has-session reports alive (so adoption applies).
func liveTmuxStub(t *testing.T) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	path, logPath = filepath.Join(dir, "tmux"), filepath.Join(dir, "tmux.log")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nfor a in \"$@\"; do case \"$a\" in new-session) echo '%1'; exit 0;; display-message) echo 0; exit 0;; esac; done\nexit 0\n"
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

func storedHooksFlag(t *testing.T, home, name string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs, _ := agentstore.Load(agentstore.FilePath(home))
		if recs[name].AttentionHooks || time.Now().After(deadline) {
			return recs[name].AttentionHooks
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFreshSpawnInjectsAttentionHooksWithoutPersistingArgs(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "hooked"})

	f.spawn(t, daemon.AgentSpawnSpec{Name: "hooked", ClaudeArgs: []string{"--base"}})

	logged := waitForLog(t, f.logPath, "new-session")
	if !strings.Contains(logged, "'--base' '--hooked' '/opt/leo dispatch report'") {
		t.Fatalf("spawned command lacks hooks:\n%s", logged)
	}
	waitAttention(t, f.store, "hooked", observe.AttentionUnknown)
	if !storedHooksFlag(t, f.sv.homePath, "hooked") {
		t.Fatal("attention_hooks not persisted on the agent record")
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

func TestResumedSpawnWithHooksIsWorking(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "resumed"})

	f.spawn(t, daemon.AgentSpawnSpec{Name: "resumed", Resumed: true})

	waitAttention(t, f.store, "resumed", observe.AttentionWorking)
}

func TestSpawnWithoutAttentionSupportLeavesAttentionAbsent(t *testing.T) {
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: false}, agentstore.Record{Name: "plain", AttentionHooks: true})
	f.store.Set("plain", observe.AttentionFinished) // stale entry from a hooked past

	f.spawn(t, daemon.AgentSpawnSpec{Name: "plain", ClaudeArgs: []string{"--base"}})

	logged := waitForLog(t, f.logPath, "new-session")
	if strings.Contains(logged, "--hooked") {
		t.Fatalf("unsupported driver got hooks:\n%s", logged)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, present := f.store.Get("plain")
		recs, _ := agentstore.Load(agentstore.FilePath(f.sv.homePath))
		if !present && !recs["plain"].AttentionHooks {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("present=%v flag=%v; want attention absent and flag cleared", present, recs["plain"].AttentionHooks)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAdoptSetsUnknownOnlyForHookedRecords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flag    bool
		present bool
	}{{"hooked", true, true}, {"unhooked", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{Name: "adopted", AttentionHooks: tc.flag})

			f.spawn(t, daemon.AgentSpawnSpec{Name: "adopted", Adopt: true})
			waitForLog(t, f.logPath, "show-options")

			if tc.present {
				waitAttention(t, f.store, "adopted", observe.AttentionUnknown)
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
		`"SessionEnd":` + hook + `,"Stop":` + hook + `,"UserPromptSubmit":` + hook + `}}'`
	if !strings.Contains(logged, "'--model' 'sonnet' "+wantSettings) {
		t.Fatalf("spawned command lacks the exact merged settings:\n%s\nwant substring:\n%s", logged, wantSettings)
	}
	if n := strings.Count(logged, "--settings"); n != 1 {
		t.Fatalf("--settings appears %d times, want 1:\n%s", n, logged)
	}
}
