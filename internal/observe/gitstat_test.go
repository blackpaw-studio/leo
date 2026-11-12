package observe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestParseShortstat(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    GitStats
		wantErr bool
	}{
		{"empty is a clean tree", "", GitStats{}, false},
		{"whitespace only", " \n", GitStats{}, false},
		{"both", " 3 files changed, 10 insertions(+), 2 deletions(-)\n", GitStats{Added: 10, Removed: 2}, false},
		{"insertions only", " 1 file changed, 4 insertions(+)\n", GitStats{Added: 4}, false},
		{"deletions only", " 2 files changed, 7 deletions(-)\n", GitStats{Removed: 7}, false},
		{"singular forms", " 1 file changed, 1 insertion(+), 1 deletion(-)\n", GitStats{Added: 1, Removed: 1}, false},
		{"binary only", " 1 file changed, 0 insertions(+), 0 deletions(-)\n", GitStats{}, false},
		{"no trailing newline", " 1 file changed, 5 insertions(+)", GitStats{Added: 5}, false},
		{"garbage", "fatal: not a git repository", GitStats{}, true},
		{"number overflow", " 1 file changed, 99999999999999999999 insertions(+)\n", GitStats{}, true},
		{"two summary lines", " 1 file changed, 1 insertion(+)\n 1 file changed, 1 insertion(+)\n", GitStats{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseShortstat(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v; wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("stats = %+v; want %+v", got, tc.want)
			}
		})
	}
}

type gitCall struct {
	argv     []string
	env      []string
	deadline time.Duration // remaining at call time; 0 = none
}

type fakeGitRunner struct {
	calls  []gitCall
	stdout string
	exit   int
	err    error
}

func (f *fakeGitRunner) Run(ctx context.Context, env []string, argv ...string) ([]byte, int, error) {
	c := gitCall{argv: slices.Clone(argv), env: slices.Clone(env)}
	if dl, ok := ctx.Deadline(); ok {
		c.deadline = time.Until(dl)
	}
	f.calls = append(f.calls, c)
	return []byte(f.stdout), f.exit, f.err
}

func TestGitStatsForRunsPinnedArgvAndEnv(t *testing.T) {
	r := &fakeGitRunner{stdout: " 2 files changed, 9 insertions(+), 4 deletions(-)\n"}

	stats, ok, err := GitStatsFor(context.Background(), r, "/work/space")

	if err != nil || !ok || stats != (GitStats{Added: 9, Removed: 4}) {
		t.Fatalf("stats=%+v ok=%v err=%v", stats, ok, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d", len(r.calls))
	}
	wantArgv := []string{"git", "-C", "/work/space", "-c", "core.fsmonitor=false", "diff", "HEAD", "--shortstat"}
	if !slices.Equal(r.calls[0].argv, wantArgv) {
		t.Fatalf("argv = %q\nwant   %q", r.calls[0].argv, wantArgv)
	}
	if !slices.Contains(r.calls[0].env, "GIT_OPTIONAL_LOCKS=0") {
		t.Fatalf("env lacks GIT_OPTIONAL_LOCKS=0 (len %d)", len(r.calls[0].env))
	}
	if d := r.calls[0].deadline; d <= 0 || d > GitStatsTimeout {
		t.Fatalf("deadline remaining = %v; want within (0, %v]", d, GitStatsTimeout)
	}
}

func TestGitStatsForNotARepoIsNotAnError(t *testing.T) {
	// 128: not a repository, or no HEAD yet (fresh repo, no commits).
	for _, exit := range []int{128, 129} {
		r := &fakeGitRunner{exit: exit, stdout: "fatal: ambiguous argument 'HEAD'"}
		_, ok, err := GitStatsFor(context.Background(), r, "/work/space")
		if ok || err != nil {
			t.Fatalf("exit %d: ok=%v err=%v; want neither", exit, ok, err)
		}
	}
}

func TestGitStatsForReportsUnexpectedFailures(t *testing.T) {
	cases := map[string]*fakeGitRunner{
		"run error":      {err: errors.New("exec: git: not found")},
		"garbage output": {stdout: "huh"},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok, err := GitStatsFor(context.Background(), r, "/w"); ok || err == nil {
				t.Fatalf("ok=%v err=%v; want a failure", ok, err)
			}
		})
	}
}

func TestGitStatsForRejectsUnusableWorkspace(t *testing.T) {
	for _, ws := range []string{"", "relative/dir", "-cevil"} {
		r := &fakeGitRunner{}
		if _, ok, err := GitStatsFor(context.Background(), r, ws); ok || err == nil || len(r.calls) != 0 {
			t.Fatalf("ws %q: ok=%v err=%v calls=%d; want refused without running git", ws, ok, err, len(r.calls))
		}
	}
}

// The real runner against a real repository: a mocked seam cannot catch an
// argv git itself would refuse.
func TestExecGitRunnerAgainstRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")

	// No commits yet: no HEAD.
	if _, ok, err := GitStatsFor(context.Background(), ExecCommandRunner{}, dir); ok || err != nil {
		t.Fatalf("fresh repo: ok=%v err=%v", ok, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	stats, ok, err := GitStatsFor(context.Background(), ExecCommandRunner{}, dir)
	if err != nil || !ok || stats != (GitStats{}) {
		t.Fatalf("clean: %+v ok=%v err=%v", stats, ok, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\nTWO\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stats, ok, err = GitStatsFor(context.Background(), ExecCommandRunner{}, dir)
	if err != nil || !ok || stats != (GitStats{Added: 2, Removed: 1}) {
		t.Fatalf("dirty: %+v ok=%v err=%v", stats, ok, err)
	}

	if _, ok, err := GitStatsFor(context.Background(), ExecCommandRunner{}, t.TempDir()); ok || err != nil {
		t.Fatalf("not a repo: ok=%v err=%v", ok, err)
	}
}
