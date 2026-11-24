package consult

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// confFixture lays out a workspace, a sibling workspace, a worktrees dir and a
// claude scratch dir under one real temp root, with fake git probes keyed by
// repo root.
type confFixture struct {
	root, rawRoot, ws, other, worktrees, tmp string
	repos                                    map[string]string // repo toplevel -> common dir
	owners                                   map[string]string // dispatch id -> caller
	gitErr                                   bool
	harness                                  string
}

func newConfFixture(t *testing.T) *confFixture {
	t.Helper()
	rawRoot := t.TempDir()
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatal(err)
	}
	f := &confFixture{
		root: root, rawRoot: rawRoot, ws: filepath.Join(root, "agents", "leo"), other: filepath.Join(root, "agents", "marketing"),
		worktrees: filepath.Join(root, "state", "worktrees"), tmp: filepath.Join(root, "tmp"),
		repos: map[string]string{}, owners: map[string]string{}, harness: "claude",
	}
	for _, d := range []string{f.ws, f.other, f.worktrees, f.tmp, filepath.Join(f.ws, "pkg")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *confFixture) mk(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *confFixture) repoOf(dir string) (string, bool) {
	best := ""
	for top := range f.repos {
		if within(dir, top) && len(top) > len(best) {
			best = top
		}
	}
	return best, best != ""
}

func (f *confFixture) deps() confinementDeps {
	return confinementDeps{
		evalSymlinks: filepath.EvalSymlinks,
		gitCommonDir: func(dir string) (string, error) {
			top, ok := f.repoOf(dir)
			if f.gitErr || !ok {
				return "", errors.New("not a git repository")
			}
			return f.repos[top], nil
		},
		gitTopLevel: func(dir string) (string, error) {
			top, ok := f.repoOf(dir)
			if f.gitErr || !ok {
				return "", errors.New("not a git repository")
			}
			return top, nil
		},
		worktreeOwner: func(id string) (string, bool) { c, ok := f.owners[id]; return c, ok },
		tmpDir:        f.tmp,
		uid:           501,
	}
}

func (f *confFixture) check(cwd string) confinementVerdict {
	return checkConfinement(context.Background(), f.deps(), confinementInput{
		Cwd: cwd, RootCaller: "leo", Home: CallerHome{Workspace: f.ws, Harness: f.harness}, WorktreesDir: f.worktrees,
	})
}

func TestCheckConfinement(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, f *confFixture) string // returns cwd
		want     bool
		wantRule string
	}{
		{"workspace itself", func(t *testing.T, f *confFixture) string { return f.ws }, true, "workspace"},
		{"nested in workspace", func(t *testing.T, f *confFixture) string { return filepath.Join(f.ws, "pkg") }, true, "workspace"},
		{"sibling sharing a name prefix", func(t *testing.T, f *confFixture) string { return f.mk(t, f.ws+"-evil") }, false, ""},
		{"other agent's workspace", func(t *testing.T, f *confFixture) string { return f.other }, false, ""},
		{"dotdot out of workspace", func(t *testing.T, f *confFixture) string { return filepath.Join(f.ws, "..", "marketing") }, false, ""},
		{"symlink inside workspace to another workspace", func(t *testing.T, f *confFixture) string {
			link := filepath.Join(f.ws, "link")
			if err := os.Symlink(f.other, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, false, ""},
		{"symlink outside pointing into workspace", func(t *testing.T, f *confFixture) string {
			link := filepath.Join(f.root, "alias")
			if err := os.Symlink(filepath.Join(f.ws, "pkg"), link); err != nil {
				t.Fatal(err)
			}
			return link
		}, true, "workspace"},
		{"filesystem root", func(t *testing.T, f *confFixture) string { return "/" }, false, ""},
		{"sibling worktree of the workspace repo", func(t *testing.T, f *confFixture) string {
			common := f.mk(t, f.ws, ".git")
			f.repos[f.ws], f.repos[f.mk(t, f.root, "checkouts", "leo-wt")] = common, common
			return filepath.Join(f.root, "checkouts", "leo-wt")
		}, true, "git worktree"},
		{"checkout of a different repo", func(t *testing.T, f *confFixture) string {
			f.repos[f.ws], f.repos[f.other] = f.mk(t, f.ws, ".git"), f.mk(t, f.other, ".git")
			return f.other
		}, false, ""},
		{"git error denies", func(t *testing.T, f *confFixture) string {
			common := f.mk(t, f.ws, ".git")
			f.repos[f.ws], f.repos[f.other] = common, common
			f.gitErr = true
			return f.other
		}, false, ""},
		{"workspace nested in a larger repo grants nothing", func(t *testing.T, f *confFixture) string {
			f.repos[f.mk(t, f.root, "agents")] = f.mk(t, f.root, "agents", ".git")
			return f.other
		}, false, ""},
		{"own dispatch worktree", func(t *testing.T, f *confFixture) string {
			f.owners["d-1"] = "leo"
			return f.mk(t, f.worktrees, "d-1", "sub")
		}, true, "dispatch worktree"},
		{"another caller's dispatch worktree", func(t *testing.T, f *confFixture) string {
			f.owners["d-2"] = "marketing"
			return f.mk(t, f.worktrees, "d-2")
		}, false, ""},
		{"unknown dispatch worktree", func(t *testing.T, f *confFixture) string { return f.mk(t, f.worktrees, "d-3") }, false, ""},
		{"worktrees dir dotdot escape", func(t *testing.T, f *confFixture) string {
			f.owners["d-1"] = "leo"
			return filepath.Join(f.worktrees, "d-1", "..", "..", "..", "agents", "marketing")
		}, false, ""},
		{"worktrees dir itself", func(t *testing.T, f *confFixture) string { return f.worktrees }, false, ""},
		{"claude scratch, raw workspace key", func(t *testing.T, f *confFixture) string {
			return f.mk(t, f.tmp, "claude-501", claudeScratchKey(f.ws), "scratchpad")
		}, true, "scratch"},
		{"claude scratch, other uid", func(t *testing.T, f *confFixture) string {
			return f.mk(t, f.tmp, "claude-502", claudeScratchKey(f.ws))
		}, false, ""},
		{"claude scratch of another workspace", func(t *testing.T, f *confFixture) string {
			return f.mk(t, f.tmp, "claude-501", claudeScratchKey(f.other))
		}, false, ""},
		{"codex gets no scratch root", func(t *testing.T, f *confFixture) string {
			f.harness = "codex"
			return f.mk(t, f.tmp, "claude-501", claudeScratchKey(f.ws))
		}, false, ""},
		{"opencode gets no scratch root", func(t *testing.T, f *confFixture) string {
			f.harness = "opencode"
			return f.mk(t, f.tmp, "claude-501", claudeScratchKey(f.ws))
		}, false, ""},
		{"missing cwd denies", func(t *testing.T, f *confFixture) string { return filepath.Join(f.root, "nope") }, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfFixture(t)
			cwd := tc.setup(t, f)
			got := f.check(cwd)
			if got.Allowed != tc.want {
				t.Fatalf("Allowed = %v (rule %q), want %v; roots %v", got.Allowed, got.Rule, tc.want, got.Roots)
			}
			if tc.want && !strings.Contains(got.Rule, tc.wantRule) {
				t.Fatalf("Rule = %q, want it to mention %q", got.Rule, tc.wantRule)
			}
			if len(got.Roots) == 0 {
				t.Fatalf("Roots is empty; refusal messages need the allowed roots")
			}
		})
	}
}

func TestCheckConfinementScratchKeyFromRealpath(t *testing.T) {
	f := newConfFixture(t)
	link := filepath.Join(f.root, "wslink")
	if err := os.Symlink(f.ws, link); err != nil {
		t.Fatal(err)
	}
	cwd := f.mk(t, f.tmp, "claude-501", claudeScratchKey(f.ws))
	got := checkConfinement(context.Background(), f.deps(), confinementInput{
		Cwd: cwd, RootCaller: "leo", Home: CallerHome{Workspace: link, Harness: "claude"}, WorktreesDir: f.worktrees,
	})
	if !got.Allowed {
		t.Fatalf("scratch keyed by the realpath of a symlinked workspace was refused: %+v", got)
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		p, root string
		want    bool
	}{
		{"/a/b", "/a/b", true}, {"/a/b/c", "/a/b", true}, {"/a/bc", "/a/b", false},
		{"/a", "/a/b", false}, {"/anything", "/", true}, {"/", "/", true},
	}
	for _, tc := range cases {
		if got := within(tc.p, tc.root); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.p, tc.root, got, tc.want)
		}
	}
}

func TestClaudeScratchKey(t *testing.T) {
	if got := claudeScratchKey("/Users/evan/.leo/agents/leo"); got != "-Users-evan--leo-agents-leo" {
		t.Fatalf("claudeScratchKey = %q", got)
	}
}

// TestConfinementRealGit drives real git through the production probes: it
// pins the argv (a mocked seam hid argv bugs here before) and the /var ->
// /private/var realpath handling on macOS.
func TestConfinementRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newConfFixture(t)
	rawRoot := f.rawRoot
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(f.ws, "init", "-q")
	run(f.ws, "commit", "-q", "--allow-empty", "-m", "init")
	sibling := filepath.Join(f.root, "checkouts", "leo-wt")
	run(f.ws, "worktree", "add", "-q", "-b", "wt", sibling)
	foreign := f.mk(t, f.root, "checkouts", "foreign")
	run(foreign, "init", "-q")

	var calls [][]string
	newCmd := func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		return exec.Command(name, args...)
	}
	common, top := gitProbes(newCmd)
	deps := f.deps()
	deps.gitCommonDir, deps.gitTopLevel = common, top
	// Callers hand over the unresolved form (/var/... on macOS), so probe
	// through it: git answers with /private/var/... and the two must meet.
	raw := func(p string) string { return strings.Replace(p, f.root, rawRoot, 1) }
	in := func(cwd string) confinementVerdict {
		return checkConfinement(context.Background(), deps, confinementInput{
			Cwd: raw(cwd), RootCaller: "leo", Home: CallerHome{Workspace: raw(f.ws), Harness: "claude"}, WorktreesDir: f.worktrees,
		})
	}
	if got := in(sibling); !got.Allowed || !strings.Contains(got.Rule, "git worktree") {
		t.Fatalf("sibling worktree of the workspace repo refused: %+v", got)
	}
	if got := in(foreign); got.Allowed {
		t.Fatalf("unrelated repo allowed: %+v", got)
	}
	wantArgv := []string{"-C", sibling, "rev-parse", "--path-format=absolute", "--git-common-dir"}
	found := false
	for _, c := range calls {
		if reflect.DeepEqual(c, wantArgv) {
			found = true
		}
	}
	if !found {
		t.Fatalf("git argv %v never issued; calls: %v", wantArgv, calls)
	}
	topArgv := []string{"-C", f.ws, "rev-parse", "--show-toplevel"}
	found = false
	for _, c := range calls {
		if reflect.DeepEqual(c, topArgv) {
			found = true
		}
	}
	if !found {
		t.Fatalf("git argv %v never issued; calls: %v", topArgv, calls)
	}
}

func TestClaudeScratchKeyMatchesJSUTF16Replacement(t *testing.T) {
	// claude does s.replace(/[^a-zA-Z0-9]/g, "-"), which works per UTF-16
	// code unit: é is one unit, a supplementary-plane character is two.
	cases := map[string]string{
		"/tmp/café":      "-tmp-caf-",
		"/a/\U0001F600b": "-a---b",
		"/日本":            "---",
	}
	for in, want := range cases {
		if got := claudeScratchKey(in); got != want {
			t.Errorf("claudeScratchKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckConfinementScratchSymlinks(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *confFixture) string // returns cwd
		want  bool
	}{
		{"normal scratch directory", func(t *testing.T, f *confFixture) string {
			return f.mk(t, f.tmp, "claude-501", claudeScratchKey(f.ws), "scratchpad")
		}, true},
		{"key directory is a symlink to another workspace", func(t *testing.T, f *confFixture) string {
			f.mk(t, f.tmp, "claude-501")
			if err := os.Symlink(f.other, filepath.Join(f.tmp, "claude-501", claudeScratchKey(f.ws))); err != nil {
				t.Fatal(err)
			}
			return f.other
		}, false},
		{"claude-uid directory is a symlink", func(t *testing.T, f *confFixture) string {
			inside := f.mk(t, f.other, claudeScratchKey(f.ws))
			if err := os.Symlink(f.other, filepath.Join(f.tmp, "claude-501")); err != nil {
				t.Fatal(err)
			}
			return inside
		}, false},
		{"key is a regular file", func(t *testing.T, f *confFixture) string {
			f.mk(t, f.tmp, "claude-501")
			if err := os.WriteFile(filepath.Join(f.tmp, "claude-501", claudeScratchKey(f.ws)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			return f.ws
		}, true}, // inside the workspace anyway; the file must not break the check
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfFixture(t)
			cwd := tc.setup(t, f)
			if got := f.check(cwd); got.Allowed != tc.want {
				t.Fatalf("Allowed = %v (rule %q), want %v; roots %v", got.Allowed, got.Rule, tc.want, got.Roots)
			}
		})
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// realGitDeps is the fixture's deps with the production git probes.
func realGitDeps(f *confFixture) confinementDeps {
	deps := f.deps()
	deps.gitCommonDir, deps.gitTopLevel = gitProbes(exec.Command)
	return deps
}

// TestConfinementRealGitForgedMetadata: an agent controls the files under its
// workspace, including .git, so Git's answer about the workspace must not be
// trusted unless the common dir lives inside the workspace.
func TestConfinementRealGitForgedMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	verdict := func(f *confFixture, cwd string) confinementVerdict {
		return checkConfinement(context.Background(), realGitDeps(f), confinementInput{
			Cwd: cwd, RootCaller: "leo", Home: CallerHome{Workspace: f.ws, Harness: "claude"}, WorktreesDir: f.worktrees,
		})
	}
	newForeign := func(t *testing.T, f *confFixture) string {
		foreign := f.mk(t, f.root, "foreign")
		gitIn(t, foreign, "init", "-q")
		gitIn(t, foreign, "commit", "-q", "--allow-empty", "-m", "init")
		return foreign
	}

	t.Run("gitdir file pointing at a foreign repository", func(t *testing.T) {
		f := newConfFixture(t)
		foreign := newForeign(t, f)
		if err := os.WriteFile(filepath.Join(f.ws, ".git"), []byte("gitdir: "+filepath.Join(foreign, ".git")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := verdict(f, foreign); got.Allowed {
			t.Fatalf("forged .git file granted a foreign repository: %+v", got)
		}
	})

	t.Run("in-workspace gitdir whose commondir names a foreign repository", func(t *testing.T) {
		f := newConfFixture(t)
		foreign := newForeign(t, f)
		fake := f.mk(t, f.ws, "fake.git")
		must := func(name, body string) {
			if err := os.WriteFile(filepath.Join(fake, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		must("HEAD", "ref: refs/heads/main\n")
		must("commondir", filepath.Join(foreign, ".git")+"\n")
		if err := os.WriteFile(filepath.Join(f.ws, ".git"), []byte("gitdir: "+fake+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		deps := realGitDeps(f)
		if common, err := resolvedProbe(deps, deps.gitCommonDir, f.ws); err != nil || common != filepath.Join(foreign, ".git") {
			t.Skipf("git does not follow the forged commondir here (common %q, err %v); the attack does not apply", common, err)
		}
		if got := verdict(f, foreign); got.Allowed {
			t.Fatalf("forged commondir granted a foreign repository: %+v", got)
		}
	})

	t.Run("a genuine sibling worktree is still allowed", func(t *testing.T) {
		f := newConfFixture(t)
		gitIn(t, f.ws, "init", "-q")
		gitIn(t, f.ws, "commit", "-q", "--allow-empty", "-m", "init")
		sibling := filepath.Join(f.root, "checkouts", "leo-wt")
		gitIn(t, f.ws, "worktree", "add", "-q", "-b", "wt", sibling)
		if got := verdict(f, sibling); !got.Allowed || !strings.Contains(got.Rule, "git worktree") {
			t.Fatalf("sibling worktree refused: %+v", got)
		}
	})
}
