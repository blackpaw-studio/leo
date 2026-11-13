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

func TestSharedRootAliasesAreRefused(t *testing.T) {
	root := fakeHome(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	// Relative path from a cwd inside the temp tree to the root.
	rel, err := filepath.Rel(tmp, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	cases := map[string]string{
		"symlink to root":      link,
		"symlink with dot":     link + "/.",
		"relative to root":     rel,
		"dotdot through child": filepath.Join(root, "x", ".."),
	}
	for name, ws := range cases {
		t.Run(name, func(t *testing.T) {
			if err := CheckNotSharedRoot(ws); !errors.Is(err, ErrSharedRootWorkspace) {
				t.Errorf("CheckNotSharedRoot(%q) = %v, want ErrSharedRootWorkspace", ws, err)
			}
			got, _, err := ResolveWorkspace(config.TemplateConfig{Workspace: ws}, "claude", "", "copilot")
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(root, "copilot"); filepath.Clean(got) != want {
				t.Errorf("ResolveWorkspace(%q) = %q, want %q", ws, got, want)
			}
		})
	}
	if err := CheckNotSharedRoot(filepath.Join(link, "copilot")); err != nil {
		t.Errorf("child via symlink refused: %v", err)
	}
}

func TestSharedRootCaseAlias(t *testing.T) {
	root := fakeHome(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(filepath.Dir(root)), ".LEO", "agents")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	if err := CheckNotSharedRoot(alias); !errors.Is(err, ErrSharedRootWorkspace) {
		t.Errorf("case alias: err = %v, want ErrSharedRootWorkspace", err)
	}
}

func TestReceiveRefusesSharedRootWorkspace(t *testing.T) {
	root := fakeHome(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	m, sup, home, _ := receiveManager(t)
	spec := ReceiveSpec{Record: arriving(root), From: "studio", At: receiveAt}
	if err := m.Receive(context.Background(), spec); !errors.Is(err, ErrSharedRootWorkspace) {
		t.Fatalf("Receive err = %v, want ErrSharedRootWorkspace", err)
	}
	if _, _, err := m.ArrivalEnv(spec); !errors.Is(err, ErrSharedRootWorkspace) {
		t.Fatalf("ArrivalEnv err = %v, want ErrSharedRootWorkspace", err)
	}
	if sup.spawnCall != nil {
		t.Fatal("agent started in the shared root")
	}
	if recs, _ := agentstore.Load(agentstore.FilePath(home)); len(recs) != 0 {
		t.Fatalf("record persisted: %v", recs)
	}
}

func TestSpawnNameCollisionGetsOwnWorkspace(t *testing.T) {
	root := fakeHome(t)
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	cfg.Templates["coding"] = config.TemplateConfig{Workspace: root, Model: "sonnet"}
	sup := &capturingSupervisor{}
	m := envManager(cfg, sup)
	first, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name {
		t.Fatalf("names collide: %q", first.Name)
	}
	if got, want := loadRec(t, home, second.Name).Workspace, filepath.Join(root, second.Name); got != want {
		t.Errorf("second workspace = %q, want %q", got, want)
	}
}
