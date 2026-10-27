package agentmove

import (
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

func TestRsyncSessionArgvRelative(t *testing.T) {
	got := SessionRsyncArgv(testShell, "/Users/me/.claude", []string{"projects/-Users-me-ws/", "file-history/s1/"}, "u@h:/Users/me/.claude-work")
	want := []string{"-a", "-R", "-u", "--itemize-changes",
		"-e", testShell,
		"/Users/me/.claude/./projects/-Users-me-ws/", "/Users/me/.claude/./file-history/s1/",
		"u@h:/Users/me/.claude-work/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q\nwant %q", got, want)
	}
}

func TestStagingRsyncArgv(t *testing.T) {
	got := StagingRsyncArgv(testShell, "/s/moves/a/staging", "u@h:/d/moves/a/staging")
	want := []string{"-a", "--delete", "-e", testShell, "/s/moves/a/staging/", "u@h:/d/moves/a/staging/"}
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
