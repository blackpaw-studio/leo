package agentmove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/git/gittest"
	"github.com/blackpaw-studio/leo/internal/session"
)

const destGitDir = "/Users/me/canon/.git/worktrees/web"

// useWorkspace points the fixture's agent at ws (with its transcript) and
// the destination at a canonical that holds the source's history.
func (f *moveFixture) useWorkspace(t *testing.T, ws, branch, canonical string) {
	t.Helper()
	f.ws = ws
	proj := filepath.Join(f.claude, "projects", session.ProjectSlug(ws))
	gittest.Write(t, filepath.Join(proj, "sid-1.jsonl"), "{}\n")
	f.rec.Workspace, f.rec.Branch, f.rec.CanonicalPath = ws, branch, canonical
	if err := agentstore.Save(f.home, f.rec); err != nil {
		t.Fatal(err)
	}
	f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true,
		SessionRoot: "/Users/me/.claude-work", StagingDir: destStage, WSState: WSAbsent,
		Git: &GitPreflight{GitPath: "/usr/bin/git", CanonicalPresent: true, HasRoot: true}})
	f.ep.answer[VerbGitPrepare] = jsonAnswer(GitPrepareResult{ModulesDir: destGitDir + "/modules", GitDir: destGitDir})
}

// newWorktreeFixture is "web" on a linked worktree (branch feat) with a
// staged and an unstaged change.
func newWorktreeFixture(t *testing.T) (*moveFixture, string) {
	t.Helper()
	f := newMoveFixture(t)
	repo := gittest.NewRepo(t)
	ws := filepath.Join(filepath.Dir(repo), "wt-web")
	gittest.Run(t, repo, "worktree", "add", "-q", "-b", "feat", ws)
	gittest.Write(t, filepath.Join(ws, "a.txt"), "staged\n")
	gittest.Run(t, ws, "add", "a.txt")
	gittest.Write(t, filepath.Join(ws, "b.txt"), "unstaged\n")
	f.useWorkspace(t, ws, "feat", repo)
	return f, repo
}

func TestMoveWorktreeHappyPathOrder(t *testing.T) {
	f, repo := newWorktreeFixture(t)
	head := gittest.Run(t, f.ws, "rev-parse", "HEAD")
	if _, err := f.move(t, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	stage := StagingDir(f.home, "web")
	want := []string{
		"lock web move",
		"verb preflight",
		"stop web wake=false",
		"hooks pre_send /bin/export",
		"run git -C " + f.ws + " push u@laptop:" + repo + " +" + head + ":refs/leo-move/web/head +refs/leo-move/web/index:refs/leo-move/web/index",
		"verb git-prepare",
		"run rsync -a --delete --itemize-changes --stats -e ssh -o BatchMode=yes --exclude=node_modules/ --exclude=/.worktrees/ --exclude=/.git " + f.ws + "/ u@laptop:" + f.ws + "/",
		"run rsync -a -R -u --itemize-changes -e ssh -o BatchMode=yes projects/" + session.ProjectSlug(f.ws) + "/ u@laptop:/Users/me/.claude-work/ (in " + f.claude + ")",
		"run rsync -a --delete -e ssh -o BatchMode=yes " + stage + "/ u@laptop:" + destStage + "/",
		"fence web laptop",
		"verb commit",
		"mark web laptop laptop",
		"unlock web",
	}
	if got := f.log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	g := f.ep.manifests[len(f.ep.manifests)-1].Git
	if g == nil || g.Mode != GitModeLinked || g.Canonical != repo || g.HeadRef != "refs/heads/feat" || g.HeadSHA != head || g.IndexCommit == "" {
		t.Fatalf("commit manifest git = %+v", g)
	}
	if tree := gittest.Run(t, repo, "rev-parse", g.IndexCommit+"^{tree}"); tree != gittest.Run(t, f.ws, "write-tree") {
		t.Fatal("index snapshot does not hold the staged index")
	}
	if sha, _ := git.RefSHA(context.Background(), repo, MoveRef("web", "index")); sha != "" {
		t.Fatalf("temp ref left behind: %s", sha)
	}
}

// Git-stage failures leave the source stopped and unmarked, the temp ref
// deleted and the lock released; nothing in the workspace's git state moves.
func TestMoveWorktreeFailures(t *testing.T) {
	refuse := func(code string) func(Manifest) ([]byte, error) {
		return func(Manifest) ([]byte, error) { return nil, &VerbError{Code: code, Message: "no"} }
	}
	cases := []struct {
		name    string
		arrange func(f *moveFixture)
		code    string
	}{
		{"snapshot", func(f *moveFixture) {
			// A merge starts between the P0 checks and the snapshot.
			f.prober.hook = func() {
				p := gittest.Run(t, f.ws, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
				gittest.Write(t, p, gittest.Run(t, f.ws, "rev-parse", "HEAD")+"\n")
			}
		}, CodeGitOpInProgress},
		{"push", func(f *moveFixture) {
			f.runner.answer = func(c Cmd) ([]byte, error) {
				if c.Name == "git" {
					return nil, errors.New("git push: exit 128")
				}
				return nil, nil
			}
		}, CodeMoveFailed},
		{"prepare", func(f *moveFixture) { f.ep.answer[VerbGitPrepare] = refuse(CodeBranchDiverged) }, CodeBranchDiverged},
		{"finalize", func(f *moveFixture) { f.ep.answer[VerbCommit] = refuse(CodeSubmoduleTorn) }, CodeSubmoduleTorn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, repo := newWorktreeFixture(t)
			before := gittest.Status(t, f.ws)
			tc.arrange(f)
			_, err := f.move(t, false)
			if CodeOf(err) != tc.code || !strings.Contains(err.Error(), "leo agent start web") {
				t.Fatalf("move = %v, want a recoverable %s", err, tc.code)
			}
			if f.log.has("mark ") || f.src.locked("web") {
				t.Fatalf("events %v: marked or still locked", f.log.all())
			}
			if sha, _ := git.RefSHA(context.Background(), repo, MoveRef("web", "index")); sha != "" {
				t.Fatal("temp ref left behind")
			}
			if after := gittest.Status(t, f.ws); after != before {
				t.Fatalf("workspace status changed:\n%s\nwas:\n%s", after, before)
			}
		})
	}
}

// A main-mode superproject with absorbed submodules: S5m mirrors its
// modules dir, and a failed S5m deletes nothing on the source.
func TestMoveSubmodulesModulesRsync(t *testing.T) {
	newSuper := func(t *testing.T) *moveFixture {
		f := newMoveFixture(t)
		super := gittest.SuperWithSubmodules(t)
		gittest.Run(t, super, "worktree", "add", "-q", "-b", "side", filepath.Join(filepath.Dir(super), "side"))
		f.useWorkspace(t, super, "", "")
		f.ep.answer[VerbGitPrepare] = jsonAnswer(GitPrepareResult{ModulesDir: super + "/.git/modules", GitDir: super + "/.git"})
		return f
	}
	t.Run("ok", func(t *testing.T) {
		f := newSuper(t)
		if _, err := f.move(t, false); err != nil {
			t.Fatalf("move: %v", err)
		}
		src, err := git.ModulesDir(context.Background(), f.ws)
		if err != nil {
			t.Fatal(err)
		}
		want := "run rsync -a --delete --itemize-changes -e ssh -o BatchMode=yes " + src + "/ u@laptop:" + f.ws + "/.git/modules/"
		events := f.log.all()
		if i := indexOf(events, want); i < 0 || events[i+1] != "fence web laptop" || events[i+2] != "verb commit" {
			t.Fatalf("events:\n%s\nwant %q just before the fence and commit", strings.Join(events, "\n"), want)
		}
		if !f.log.has("run rsync -a --delete --itemize-changes --stats -e ssh -o BatchMode=yes --exclude=node_modules/ --exclude=/.worktrees/ --exclude=/.git --exclude=/libs/a/deep/.git --exclude=/libs/a/.git ") {
			t.Fatalf("workspace rsync lacks the absorbed submodules' .git excludes:\n%s", strings.Join(events, "\n"))
		}
		if f.ep.manifests[len(f.ep.manifests)-1].Git.Mode != GitModeMain {
			t.Fatal("not a main-mode move")
		}
	})
	t.Run("fails", func(t *testing.T) {
		f := newSuper(t)
		f.runner.answer = func(c Cmd) ([]byte, error) {
			if strings.Contains(c.String(), "/.git/modules/") {
				return nil, errors.New("rsync: exit 23")
			}
			return nil, nil
		}
		_, err := f.move(t, false)
		if err == nil || !strings.Contains(err.Error(), "leo agent start web") || f.log.has("mark ") || f.src.locked("web") {
			t.Fatalf("move = %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.ws, ".git", "modules", "libs", "a", "HEAD")); err != nil {
			t.Fatalf("source modules dir damaged: %v", err)
		}
	})
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

// A plain repository whose destination has linked worktrees moves in main
// mode: the second preflight says so, and the destination keeps its .git.
func TestMovePlainPromotedToMain(t *testing.T) {
	f := newMoveFixture(t)
	repo := gittest.NewRepo(t)
	f.useWorkspace(t, repo, "", "")
	calls := 0
	f.ep.answer[VerbPreflight] = func(m Manifest) ([]byte, error) {
		calls++
		resp := PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: destStage,
			Git: &GitPreflight{GitPath: "/usr/bin/git", CanonicalPresent: true, HasRoot: true, LinkedWorktrees: 1}}
		return jsonAnswer(resp)(m)
	}
	f.ep.answer[VerbGitPrepare] = jsonAnswer(GitPrepareResult{ModulesDir: repo + "/.git/modules", GitDir: repo + "/.git"})
	if _, err := f.move(t, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	if calls != 2 || f.ep.manifests[0].Git.Mode != GitModePlain || f.ep.manifests[1].Git.Mode != GitModeMain {
		t.Fatalf("preflights = %d, modes %v", calls, []string{f.ep.manifests[0].Git.Mode, f.ep.manifests[1].Git.Mode})
	}
	if !f.log.has("verb git-prepare") || !f.log.has("run git -C "+repo+" push") {
		t.Fatalf("events = %v, want a push and git-prepare", f.log.all())
	}
}

func TestMoveSeedsMissingCanonical(t *testing.T) {
	f, _ := newWorktreeFixture(t)
	seeded := false
	f.ep.answer[VerbGitSeed] = func(Manifest) ([]byte, error) { seeded = true; return []byte("{}"), nil }
	f.ep.answer[VerbPreflight] = func(m Manifest) ([]byte, error) {
		g := &GitPreflight{GitPath: "/usr/bin/git", CanonicalPresent: seeded, Cloneable: !seeded, HasRoot: seeded}
		return jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: destStage, Git: g})(m)
	}
	if _, err := f.move(t, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	events := f.log.all()
	if events[1] != "verb preflight" || events[2] != "verb git-seed" || events[3] != "verb preflight" || events[4] != "stop web wake=false" {
		t.Fatalf("events = %v, want preflight, seed, preflight, then stop", events)
	}
}

func TestMoveDivergedRefusedBeforeStop(t *testing.T) {
	f, _ := newWorktreeFixture(t)
	f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: destStage,
		Git: &GitPreflight{GitPath: "/usr/bin/git", CanonicalPresent: true, HasRoot: true, BranchSHA: strings.Repeat("ab", 20)}})
	if _, err := f.move(t, false); !HasCode(err, CodeBranchDiverged) || f.log.has("stop ") {
		t.Fatalf("move onto a branch with unknown commits = %v (events %v)", err, f.log.all())
	}
	// A destination branch behind the source is a fast-forward.
	f, repo := newWorktreeFixture(t)
	old := gittest.Run(t, repo, "rev-parse", "main")
	f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: destStage,
		Git: &GitPreflight{GitPath: "/usr/bin/git", CanonicalPresent: true, HasRoot: true, BranchSHA: old}})
	if _, err := f.move(t, false); err != nil {
		t.Fatalf("fast-forward move: %v", err)
	}
}
