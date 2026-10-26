package consult

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseLsofCwdsReadsFieldOutput(t *testing.T) {
	out := "p1\ncinit\nn/\np123\ncnode\nn/Users/evan/wt/d-1\np124\ncmy tool\nn/tmp/a b\n"
	got := parseLsofCwds([]byte(out))
	want := []procCwd{
		{PID: 1, Comm: "init", Cwd: "/"},
		{PID: 123, Comm: "node", Cwd: "/Users/evan/wt/d-1"},
		{PID: 124, Comm: "my tool", Cwd: "/tmp/a b"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseLsofCwdsSkipsIncompleteProcesses(t *testing.T) {
	// pid 7 has no cwd line (unreadable); a malformed pid line starts nothing.
	out := "p7\ncghost\npbogus\ncx\nn/nowhere\np8\ncok\nn/ok\n"
	got := parseLsofCwds([]byte(out))
	want := []procCwd{{PID: 8, Comm: "ok", Cwd: "/ok"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func shCmd(script string) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "lsof" || !slices.Equal(args, []string{"-nP", "-a", "-d", "cwd", "-Fpcn"}) {
			panic("unexpected lsof argv: " + name)
		}
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
}

func TestRunLsofCwdsAcceptsExitOneWithPartialOutput(t *testing.T) {
	got, err := runLsofCwds(context.Background(), shCmd("printf 'p5\\ncsh\\nn/x\\n'; exit 1"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []procCwd{{PID: 5, Comm: "sh", Cwd: "/x"}}; !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRunLsofCwdsWithNoEntriesIsAnError(t *testing.T) {
	for name, script := range map[string]string{"exit 1": "exit 1", "exit 0": "exit 0", "garbage": "echo nope; exit 0"} {
		t.Run(name, func(t *testing.T) {
			if got, err := runLsofCwds(context.Background(), shCmd(script)); err == nil {
				t.Fatalf("got %+v, want an error", got)
			}
		})
	}
}

func TestRunLsofCwdsReportsAMissingBinary(t *testing.T) {
	missing := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/lsof")
	}
	if _, err := runLsofCwds(context.Background(), missing); err == nil {
		t.Fatal("want an error")
	}
}

func TestReadProcCwdsSkipsWhatItCannotRead(t *testing.T) {
	proc := t.TempDir()
	mk := func(pid, cwd, comm string) {
		dir := filepath.Join(proc, pid)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if cwd != "" {
			if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
				t.Fatal(err)
			}
		}
		if comm != "" {
			if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("123", "/work/d-1", "node\n")
	mk("456", "/", "")         // comm unreadable: still a cwd holder
	mk("789", "", "denied\n")  // cwd unreadable (another user's process)
	mk("self", "/nope", "x\n") // not a pid
	if err := os.WriteFile(filepath.Join(proc, "cpuinfo"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readProcCwds(proc)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b procCwd) int { return a.PID - b.PID })
	want := []procCwd{{PID: 123, Comm: "node", Cwd: "/work/d-1"}, {PID: 456, Cwd: "/"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestReadProcCwdsFailsWhenProcIsUnreadable(t *testing.T) {
	if _, err := readProcCwds(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("want an error")
	}
}
