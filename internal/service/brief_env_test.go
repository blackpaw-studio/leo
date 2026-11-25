package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestSessionEnvArgsCarriesTheLaunchBriefSwitch(t *testing.T) {
	on := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent, brief: true}, nil), " ")
	off := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent}, nil), " ")

	if !strings.Contains(on, "-e LEO_BRIEF=1") {
		t.Errorf("env with briefs on = %s, want LEO_BRIEF=1", on)
	}
	if strings.Contains(off, "LEO_BRIEF") {
		t.Errorf("env with briefs off = %s, want no LEO_BRIEF", off)
	}
}

func TestSessionEnvArgsIgnoresSpecEnvBriefVar(t *testing.T) {
	args := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent, Env: map[string]string{"LEO_BRIEF": "1"}}, nil), " ")
	if strings.Contains(args, "LEO_BRIEF") {
		t.Errorf("env = %s, want leo's own switch to be the only source", args)
	}
}

func TestSupervisorReadsTheBriefSwitchFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	sv := &Supervisor{configPath: path}
	if sv.briefEnabled() {
		t.Error("no config must read as off")
	}
	if err := os.WriteFile(path, []byte("brief:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !sv.briefEnabled() {
		t.Error("brief.enabled: true must read as on")
	}
}

// briefSupervisor is a supervisor whose home holds agent a with a stored
// brief, and whose config has briefs on or off.
func briefSupervisor(t *testing.T, on bool) *Supervisor {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := "brief:\n  enabled: false\n"
	if on {
		cfg = "brief:\n  enabled: true\n"
	}
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agentstore.Save(home, agentstore.Record{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := agentstore.SetBrief(home, "a", &brief.Brief{Goal: "ship it", Open: []string{"review"}}); err != nil {
		t.Fatal(err)
	}
	return &Supervisor{homePath: home, configPath: cfgPath}
}

// A respawn announces the agent to every stream consumer, who then lose its
// brief unless the announcement carries it.
func TestSpawnedAgentViewCarriesTheStoredBriefWhileBriefsAreOn(t *testing.T) {
	got := briefSupervisor(t, true).spawnedAgentView(daemon.AgentSpawnSpec{Name: "a"}, time.Now())
	if got.Brief == nil || got.Brief.Goal != "ship it" || len(got.Brief.Open) != 1 {
		t.Fatalf("brief = %+v, want the stored one", got.Brief)
	}
	if off := briefSupervisor(t, false).spawnedAgentView(daemon.AgentSpawnSpec{Name: "a"}, time.Now()); off.Brief != nil {
		t.Errorf("brief with briefs off = %+v, want none", off.Brief)
	}
	sv := briefSupervisor(t, true)
	if none := sv.spawnedAgentView(daemon.AgentSpawnSpec{Name: "unknown"}, time.Now()); none.Brief != nil {
		t.Errorf("brief of an agent with none = %+v", none.Brief)
	}
}

func TestSpawnedAgentViewBriefIsACopy(t *testing.T) {
	sv := briefSupervisor(t, true)
	got := sv.spawnedAgentView(daemon.AgentSpawnSpec{Name: "a"}, time.Now())
	got.Brief.Open[0] = "changed"
	if again := sv.spawnedAgentView(daemon.AgentSpawnSpec{Name: "a"}, time.Now()); again.Brief.Open[0] != "review" {
		t.Fatalf("a consumer's edit reached the stored brief: %+v", again.Brief)
	}
}

// A live rename publishes the old name stopping and the new one appearing; the
// record is still filed under the old name at that moment.
func TestRenameAgentAnnouncesTheStoredBrief(t *testing.T) {
	origRename := tmuxRenameSession
	tmuxRenameSession = func(tmuxPath, old, new string) error { return nil }
	defer func() { tmuxRenameSession = origRename }()

	for _, on := range []bool{true, false} {
		sv := briefSupervisor(t, on)
		s := newTestSupervisor(t, "a")
		s.homePath, s.configPath = sv.homePath, sv.configPath
		pub := &recordingPublisher{}
		s.SetPublisher(pub)

		if err := s.RenameAgent("a", "b"); err != nil {
			t.Fatalf("RenameAgent: %v", err)
		}

		events := pub.Events()
		spawned, ok := events[len(events)-1].Payload.(*observe.AgentSpawnedPayload)
		if !ok || spawned.Agent.Name != "b" {
			t.Fatalf("on=%v: last event = %+v", on, events[len(events)-1])
		}
		if on && (spawned.Agent.Brief == nil || spawned.Agent.Brief.Goal != "ship it") {
			t.Errorf("renamed agent's brief = %+v, want the stored one", spawned.Agent.Brief)
		}
		if !on && spawned.Agent.Brief != nil {
			t.Errorf("renamed agent's brief with briefs off = %+v", spawned.Agent.Brief)
		}
	}
}
