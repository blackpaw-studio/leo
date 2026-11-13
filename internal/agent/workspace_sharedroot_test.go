package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// fakeHome points $HOME at a temp dir and returns the shared agents root
// (<home>/.leo/agents) under it.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".leo", "agents")
}

func TestResolveWorkspaceRepolessNeverUsesSharedRoot(t *testing.T) {
	root := fakeHome(t)
	dedicated := filepath.Join(t.TempDir(), "dedicated")

	cases := []struct {
		name         string
		workspace    string
		tmplName     string
		nameOverride string
		want         string
	}{
		{"default root, template name", "", "claude", "", filepath.Join(root, "claude")},
		{"default root, name override", "", "claude", "copilot", filepath.Join(root, "copilot")},
		{"explicit root spelled with tilde", "~/.leo/agents", "claude", "copilot", filepath.Join(root, "copilot")},
		{"explicit root with trailing slash", root + "/", "claude", "copilot", filepath.Join(root, "copilot")},
		{"dedicated workspace used as-is", dedicated, "claude", "copilot", dedicated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, name, err := ResolveWorkspace(config.TemplateConfig{Workspace: tc.workspace}, tc.tmplName, "", tc.nameOverride)
			if err != nil {
				t.Fatal(err)
			}
			if ws != tc.want {
				t.Errorf("workspace = %q, want %q", ws, tc.want)
			}
			if name == "" {
				t.Error("empty agent name")
			}
			if fi, err := os.Stat(ws); err != nil || !fi.IsDir() {
				t.Errorf("workspace not created: %v", err)
			}
		})
	}
}

func TestResolveWorkspaceRepolessRejectsEscapingNames(t *testing.T) {
	fakeHome(t)
	for _, n := range []string{"..", ".", "a/b", `a\b`} {
		if _, _, err := ResolveWorkspace(config.TemplateConfig{}, "claude", "", n); err == nil {
			t.Errorf("name %q accepted; would escape the shared root", n)
		}
	}
}

func TestCheckNotSharedRoot(t *testing.T) {
	root := fakeHome(t)
	if err := CheckNotSharedRoot(root); !errors.Is(err, ErrSharedRootWorkspace) {
		t.Errorf("root: err = %v, want ErrSharedRootWorkspace", err)
	}
	if err := CheckNotSharedRoot(root + "/./"); !errors.Is(err, ErrSharedRootWorkspace) {
		t.Errorf("unclean root: err = %v, want ErrSharedRootWorkspace", err)
	}
	if err := CheckNotSharedRoot(filepath.Join(root, "copilot")); err != nil {
		t.Errorf("subdir refused: %v", err)
	}
}

func TestSpawnRefusesSharedRootWorkspace(t *testing.T) {
	root := fakeHome(t)
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	cfg.Templates["coding"] = config.TemplateConfig{Workspace: root, Model: "sonnet"}
	sup := &capturingSupervisor{}
	m := envManager(cfg, sup)

	// "." bypasses Spawn's ValidateRepo only by calling the shared flow
	// directly; it resolves to the root itself.
	_, err := m.spawnShared(cfg, cfg.Templates["coding"], SpawnSpec{Template: "coding", Repo: "."})
	if !errors.Is(err, ErrSharedRootWorkspace) {
		t.Fatalf("err = %v, want ErrSharedRootWorkspace", err)
	}
	if sup.spawnCall != nil {
		t.Fatal("agent was spawned in the shared root")
	}

	// The public path: repo-less spawn lands in <root>/<name>.
	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Name: "copilot"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loadRec(t, home, rec.Name).Workspace, filepath.Join(root, "copilot"); got != want {
		t.Errorf("stored workspace = %q, want %q", got, want)
	}
}

// An existing record whose workspace is the shared root (a legacy agent) must
// still start; only new spawns are refused.
func TestStartExistingSharedRootRecordStillWorks(t *testing.T) {
	root := fakeHome(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	sup := &capturingSupervisor{}
	m := envManager(cfg, sup)
	if err := agentstore.Save(home, agentstore.Record{Name: "documentary", Template: "coding", Workspace: root, Stopped: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start("documentary"); err != nil {
		t.Fatalf("Start of legacy root-workspace record failed: %v", err)
	}
	if sup.spawnCall == nil || sup.spawnCall.WorkDir != root {
		t.Fatalf("spawn call = %+v, want WorkDir %s", sup.spawnCall, root)
	}
}
