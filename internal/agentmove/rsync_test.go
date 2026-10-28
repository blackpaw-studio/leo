package agentmove

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const testShell = "ssh -o BatchMode=yes -o ControlMaster=auto -o ControlPath=/s/remotes/laptop.ctl"

func TestRsyncWorkspaceArgv(t *testing.T) {
	got := WorkspaceRsyncArgv(testShell, "/Users/me/ws", "u@h:/Users/me/ws", []string{"node_modules/", "/.worktrees/"})
	want := []string{"-a", "--delete", "--itemize-changes", "--stats",
		"-e", testShell,
		"--exclude=node_modules/", "--exclude=/.worktrees/",
		"/Users/me/ws/", "u@h:/Users/me/ws/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant %q", got, want)
	}
	local := WorkspaceRsyncArgv("", "/a", "/b.leo-partial", nil)
	if want := []string{"-a", "--delete", "--itemize-changes", "--stats", "/a/", "/b.leo-partial/"}; !reflect.DeepEqual(local, want) {
		t.Fatalf("local argv = %q, want %q", local, want)
	}
}

// Session files go straight from their real source path into the real
// destination dir: no -R, so rsync never creates (or replaces) an implied
// parent such as a symlinked projects/.
func TestRsyncSessionArgv(t *testing.T) {
	got := SessionRsyncCmd(testShell, "/real/.claude/projects/-Users-me-ws/", "u@h:/real/shared/-Users-me-ws")
	want := Cmd{Name: "rsync", Args: []string{"-a", "-u", "--itemize-changes",
		"-e", testShell, "/real/.claude/projects/-Users-me-ws/", "u@h:/real/shared/-Users-me-ws/"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cmd = %+v\nwant %+v", got, want)
	}
}

// Invariant: a destination store dir that is a symlink stays a symlink and
// the files land behind it. Runs the real rsync (openrsync on macOS).
func TestRsyncSessionKeepsDestinationSymlink(t *testing.T) {
	src, dstRoot, shared := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "projects", "-ws", "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "projects", "-ws", "memory", "m.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dstRoot, "projects")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(shared, "-ws")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := SessionRsyncCmd("", filepath.Join(src, "projects", "-ws")+"/", target)
	if _, err := (ExecRunner{}).Run(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(dstRoot, "projects")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("projects is no longer a symlink: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(shared, "-ws", "memory", "m.md")); err != nil {
		t.Fatalf("file not behind the symlink: %v", err)
	}
}

func TestStagingRsyncArgv(t *testing.T) {
	got := StagingRsyncArgv(testShell, "/s/moves/a/staging", "u@h:/d/moves/a/staging")
	want := []string{"-a", "--delete", "--itemize-changes", "-e", testShell, "/s/moves/a/staging/", "u@h:/d/moves/a/staging/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestModulesRsyncArgvMoveAndCopyTarget(t *testing.T) {
	move := ModulesRsyncArgv(testShell, "/c/.git/worktrees/wt/modules", "u@h:"+ModulesTarget(false, "/c/.git/worktrees/wt2/modules", "/c/.git/worktrees/wt2"))
	if want := []string{"-a", "--delete", "--itemize-changes", "-e", testShell,
		"/c/.git/worktrees/wt/modules/", "u@h:/c/.git/worktrees/wt2/modules/"}; !reflect.DeepEqual(move, want) {
		t.Fatalf("move argv = %q\nwant %q", move, want)
	}
	copyT := ModulesTarget(true, "/c/.git/worktrees/wt3/modules", "/c/.git/worktrees/wt3")
	if copyT != "/c/.git/worktrees/wt3/leo-modules.partial" {
		t.Fatalf("copy target = %q", copyT)
	}
}

func TestWorkspaceRsyncExcludesAbsorbedSubmoduleGitFiles(t *testing.T) {
	spec := WorkspaceSpec{
		Workspace:    "/ws",
		UserExcludes: []string{"node_modules/"},
		Nested:       []string{"/ws/sub/agent", "/elsewhere"},
		GitMode:      true,
		Submodules: []Submodule{
			{Path: "libs/a", State: SubmoduleAbsorbed},
			{Path: "libs/a/deep", State: SubmoduleAbsorbed},
			{Path: "libs/inline", State: SubmoduleInline},
			{Path: "libs/uninit", State: SubmoduleUninit},
		},
		NestedWorktrees: []string{".claude/worktrees/agent-1"},
	}
	want := []string{"node_modules/", "/.worktrees/", "/sub/agent/", "/.git",
		"/libs/a/.git", "/libs/a/deep/.git", "/.claude/worktrees/agent-1/"}
	if got := spec.Excludes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Excludes = %q\nwant %q", got, want)
	}
	plain := WorkspaceSpec{Workspace: "/ws"}
	if got := plain.Excludes(); !reflect.DeepEqual(got, []string{"/.worktrees/"}) {
		t.Fatalf("plain Excludes = %q, want only /.worktrees/ (.git travels)", got)
	}
}

func TestValidatePathRejectsShellMeta(t *testing.T) {
	for _, good := range []string{"/Users/evan/.leo/agents/leo", "/a/b-c_d.e@f%g+h=i,j"} {
		if err := ValidatePath(good); err != nil {
			t.Errorf("ValidatePath(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{"", "relative/x", "/a b", `/a"b`, "/a'b", "/a$b", "/a`b", "/a;b", "/a*b", "/a\nb", "/a/../b", "/a:b"} {
		if err := ValidatePath(bad); err == nil {
			t.Errorf("ValidatePath(%q) accepted", bad)
		}
	}
}

// Only itemized change lines count: --stats' summary and rsync's chatter
// are dropped, deletions are kept.
func TestItemizedLines(t *testing.T) {
	out := []byte("sending incremental file list\n>f+++++++++ a.txt\ncd+++++++++ sub/\n*deleting   gone.txt\n.d..t...... ./\n\nNumber of files: 4 (reg: 2, dir: 2)\nTotal file size: 10 bytes\n")
	want := []string{">f+++++++++ a.txt", "cd+++++++++ sub/", "*deleting   gone.txt", ".d..t...... ./"}
	if got := ItemizedLines(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("ItemizedLines = %q, want %q", got, want)
	}
}

// macOS's /usr/bin/rsync (openrsync) prints 9-character change codes, two
// shorter than GNU rsync's; captured from openrsync protocol 29.
func TestItemizedLinesOpenrsync(t *testing.T) {
	out := []byte(">f+++++++ a\ncL+++++++ l -> a\ncd+++++++ d/\n>f+++++++ d/b\n*deleting d/b\n>f.s..... a\n")
	want := []string{">f+++++++ a", "cL+++++++ l -> a", "cd+++++++ d/", ">f+++++++ d/b", "*deleting d/b", ">f.s..... a"}
	if got := ItemizedLines(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("ItemizedLines = %q, want %q", got, want)
	}
}

func TestRsyncStageCapsLines(t *testing.T) {
	var lines []string
	for i := 0; i < MaxItemizedLines+30; i++ {
		lines = append(lines, ">f+++++++++ f")
	}
	st := newRsyncStage("rsync-workspace", lines)
	if len(st.Lines) != MaxItemizedLines || st.Total != MaxItemizedLines+30 || st.Truncated != 30 {
		t.Fatalf("stage = lines %d total %d truncated %d", len(st.Lines), st.Total, st.Truncated)
	}
}
