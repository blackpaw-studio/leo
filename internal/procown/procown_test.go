package procown

import "testing"

// table builds a process table from pid -> (ppid, comm).
func table(rows ...Proc) map[int]Proc {
	procs := map[int]Proc{}
	for _, r := range rows {
		procs[r.PID] = r
	}
	return procs
}

func TestParsePSReadsPidPpidAndCommandWithSpaces(t *testing.T) {
	out := "    1     0 /sbin/launchd\n  200   100 /Applications/Visual Studio Code.app/Contents/MacOS/Electron\n\n  300   200 sh\n"
	got, err := ParsePS([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[200].PPID != 100 || got[200].Comm != "/Applications/Visual Studio Code.app/Contents/MacOS/Electron" || got[300].PID != 300 {
		t.Fatalf("ParsePS = %+v", got)
	}
}

func TestParsePSRejectsMalformedRows(t *testing.T) {
	for _, out := range []string{"abc 1 sh\n", "1 xyz sh\n", "1\n", ""} {
		if _, err := ParsePS([]byte(out)); err == nil {
			t.Errorf("ParsePS(%q) succeeded, want an error", out)
		}
	}
}

// The agent's harness is the pane's own process, and a hook is the agent's
// when the process that fired it is that one: a hook command runs as leo
// (and a shell), so those are looked through. A child harness process that
// inherited the agent's token fires hooks of its own, and is some other
// process, however it was started.
func TestOwnsHarness(t *testing.T) {
	const root = 100
	for _, tc := range []struct {
		name  string
		procs map[int]Proc
		pid   int
		root  int
		want  bool
	}{
		{"the harness itself (a plugin)", table(Proc{PID: root, PPID: 1, Comm: "/opt/bin/opencode"}), root, root, true},
		{"leo run directly by the harness", table(Proc{PID: root, PPID: 1, Comm: "codex"}, Proc{PID: 200, PPID: root, Comm: "/usr/local/bin/leo"}), 200, root, true},
		{"leo run through sh -c", table(Proc{PID: root, PPID: 1, Comm: "codex"}, Proc{PID: 150, PPID: root, Comm: "/bin/sh"}, Proc{PID: 200, PPID: 150, Comm: "leo"}), 200, root, true},
		{"harness under a wrapper shell root", table(Proc{PID: root, PPID: 1, Comm: "/bin/zsh"}, Proc{PID: 110, PPID: root, Comm: "codex"}, Proc{PID: 200, PPID: 110, Comm: "leo"}), 200, root, true},
		{"a child harness started by a tool shell", table(Proc{PID: root, PPID: 1, Comm: "codex"}, Proc{PID: 150, PPID: root, Comm: "bash"}, Proc{PID: 160, PPID: 150, Comm: "codex"}, Proc{PID: 200, PPID: 160, Comm: "leo"}), 200, root, false},
		{"a child harness that sh exec'd, a direct child of the harness", table(Proc{PID: root, PPID: 1, Comm: "codex"}, Proc{PID: 160, PPID: root, Comm: "codex"}, Proc{PID: 200, PPID: 160, Comm: "leo"}), 200, root, false},
		{"a child plugin process", table(Proc{PID: root, PPID: 1, Comm: "opencode"}, Proc{PID: 160, PPID: root, Comm: "opencode"}), 160, root, false},
		{"a child harness under a wrapper shell root", table(Proc{PID: root, PPID: 1, Comm: "sh"}, Proc{PID: 110, PPID: root, Comm: "codex"}, Proc{PID: 150, PPID: 110, Comm: "sh"}, Proc{PID: 160, PPID: 150, Comm: "codex"}, Proc{PID: 200, PPID: 160, Comm: "leo"}), 200, root, false},
		{"a process outside the pane", table(Proc{PID: root, PPID: 1, Comm: "codex"}, Proc{PID: 500, PPID: 1, Comm: "codex"}), 500, root, false},
		{"a pid that is not running", table(Proc{PID: root, PPID: 1, Comm: "codex"}), 999, root, false},
		{"no pid reported", table(Proc{PID: root, PPID: 1, Comm: "codex"}), 0, root, false},
		{"no pane root", table(Proc{PID: root, PPID: 1, Comm: "codex"}), root, 0, false},
		{"only shells and leo up to init", table(Proc{PID: 200, PPID: 150, Comm: "leo"}, Proc{PID: 150, PPID: 1, Comm: "sh"}), 200, root, false},
		{"a process cycle", table(Proc{PID: 200, PPID: 150, Comm: "sh"}, Proc{PID: 150, PPID: 200, Comm: "sh"}), 200, root, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnsHarness(tc.procs, tc.root, tc.pid); got != tc.want {
				t.Fatalf("OwnsHarness(root %d, pid %d) = %v, want %v", tc.root, tc.pid, got, tc.want)
			}
		})
	}
}
