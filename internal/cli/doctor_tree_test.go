package cli

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// lookPathStub builds a LookPath replacement resolving only the given
// name->path pairs.
func lookPathStub(found map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := found[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestSelectTreeProbeBinaryPrefersNode(t *testing.T) {
	b, err := selectTreeProbeBinary(lookPathStub(map[string]string{
		"node":    "/opt/homebrew/bin/node",
		"python3": "/opt/homebrew/bin/python3",
	}))
	if err != nil {
		t.Fatalf("selectTreeProbeBinary: %v", err)
	}
	if b.Name != "node" || b.Path != "/opt/homebrew/bin/node" {
		t.Fatalf("selected %+v, want homebrew node", b)
	}
}

func TestSelectTreeProbeBinaryFallsBackToPython(t *testing.T) {
	b, err := selectTreeProbeBinary(lookPathStub(map[string]string{
		"python3": "/opt/homebrew/bin/python3",
	}))
	if err != nil {
		t.Fatalf("selectTreeProbeBinary: %v", err)
	}
	if b.Name != "python3" {
		t.Fatalf("selected %+v, want python3", b)
	}
}

// An Apple platform binary is exempt from Local Network TCC, so probing with
// one would report success even while third-party binaries in the same tree
// are denied — a false pass, which is the exact failure mode this probe
// exists to eliminate.
func TestSelectTreeProbeBinaryRejectsPlatformBinaries(t *testing.T) {
	for _, path := range []string{
		"/usr/bin/curl",
		"/usr/bin/python3",
		"/bin/sh",
		"/usr/sbin/foo",
		"/sbin/bar",
		"/System/Library/Frameworks/Python.framework/Versions/3.9/bin/python3",
	} {
		if !isPlatformBinary(path) {
			t.Errorf("isPlatformBinary(%q) = false, want true", path)
		}
	}
	for _, path := range []string{
		"/opt/homebrew/bin/node",
		"/usr/local/bin/node",
		"/Users/evan/.local/bin/node",
	} {
		if isPlatformBinary(path) {
			t.Errorf("isPlatformBinary(%q) = true, want false", path)
		}
	}

	// Only a platform curl available -> no usable probe binary.
	_, err := selectTreeProbeBinary(lookPathStub(map[string]string{"curl": "/usr/bin/curl"}))
	if err == nil {
		t.Fatal("expected an error when only platform binaries are available")
	}
}

// curl reports its OWN exit codes, not errno: 7 ("couldn't connect") covers
// refused, unreachable, and timed-out alike. That cannot discriminate "the
// packet left the machine" from "the OS blocked it", which is the entire basis
// of the verdict — so curl must never be selected, even a third-party one.
func TestSelectTreeProbeBinaryRejectsCurl(t *testing.T) {
	_, err := selectTreeProbeBinary(lookPathStub(map[string]string{"curl": "/opt/homebrew/bin/curl"}))
	if err == nil {
		t.Fatal("curl must not be usable as a probe binary: its exit codes cannot discriminate denial")
	}
}

func TestSelectTreeProbeBinaryErrorNamesTheFix(t *testing.T) {
	_, err := selectTreeProbeBinary(lookPathStub(nil))
	if err == nil {
		t.Fatal("expected an error when nothing is available")
	}
	if !strings.Contains(err.Error(), "brew") {
		t.Fatalf("error %q should tell the operator how to fix it", err)
	}
}

func TestTreeProbeCommandEmbedsHostAndPort(t *testing.T) {
	for _, name := range []string{"node", "python3"} {
		cmd, err := treeProbeCommand(probeBinary{Name: name, Path: "/opt/homebrew/bin/" + name}, "10.0.2.9:443")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(cmd, "10.0.2.9") {
			t.Errorf("%s command %q missing host", name, cmd)
		}
		if !strings.Contains(cmd, "443") {
			t.Errorf("%s command %q missing port", name, cmd)
		}
		// Must invoke the resolved absolute path, not rely on the tmux
		// server's PATH, which is inherited from the daemon.
		if !strings.Contains(cmd, "/opt/homebrew/bin/"+name) {
			t.Errorf("%s command %q must use the absolute path", name, cmd)
		}
	}
}

// Every variant must bound its own connect attempt, so an unreachable target
// reports a specific failure instead of burning RunInServer's whole budget.
func TestTreeProbeCommandSelfBoundsConnect(t *testing.T) {
	want := map[string]string{
		"node":    "3000",
		"python3": "settimeout(3)",
	}
	for name, needle := range want {
		cmd, err := treeProbeCommand(probeBinary{Name: name, Path: "/opt/homebrew/bin/" + name}, "10.0.2.9:443")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(cmd, needle) {
			t.Errorf("%s command %q must bound its connect (want %q)", name, cmd, needle)
		}
	}
}

func TestTreeProbeCommandRejectsBadTarget(t *testing.T) {
	if _, err := treeProbeCommand(probeBinary{Name: "node", Path: "/x/node"}, "not-a-target"); err == nil {
		t.Fatal("expected an error for a target without a port")
	}
}

func TestClassifyTreeOutput(t *testing.T) {
	tests := []struct {
		out  string
		want string
	}{
		{"OK\n", "granted"},
		{"  OK  ", "granted"},
		{"FAIL EHOSTUNREACH\n", "denied"},
		{"FAIL 65\n", "denied"},
		{"FAIL ETIMEDOUT\n", "undetermined"},
		{"FAIL 60\n", "undetermined"},
		{"FAIL ECONNREFUSED\n", "granted"},
		{"FAIL 61\n", "granted"},
		{"", "undetermined"},
		{"Traceback (most recent call last):\n  ...\n", "undetermined"},
	}
	for _, tc := range tests {
		if got := classifyTreeOutput(tc.out); got != tc.want {
			t.Errorf("classifyTreeOutput(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

// The whole point of the in-tree probe: the tree's verdict decides, and a tree
// denial while leo's own process connects fine is called out as such.
func TestCombineLocalNetworkStates(t *testing.T) {
	tests := []struct {
		name      string
		tree      string
		inProcess string
		want      string
	}{
		{"tree granted wins", "granted", "denied", "granted"},
		{"tree denied while leo connects is the tmux-tree signature", "denied", "granted", treeDeniedState},
		{"both denied is not specific to the tree", "denied", "denied", "denied"},
		{"tree denied, leo undetermined", "denied", "undetermined", "denied"},
		// A probe that RAN but was inconclusive must never inherit leo's own
		// grant: leo always holds it, so deferring would turn "we don't know
		// about the tree" into a confident "granted" — the false pass this
		// whole feature exists to eliminate.
		{"inconclusive tree does not inherit leo's grant", "undetermined", "granted", "undetermined"},
		{"neither conclusive", "undetermined", "undetermined", "undetermined"},
		// No verdict at all (no server, no probe binary) measured nothing, so
		// falling back to the in-process result is honest.
		{"unavailable tree falls back to in-process", "", "granted", "granted"},
		{"unavailable tree falls back to in-process denial", "", "denied", "denied"},
	}
	for _, tc := range tests {
		if got := combineLocalNetworkStates(tc.tree, tc.inProcess); got != tc.want {
			t.Errorf("%s: combine(%q, %q) = %q, want %q", tc.name, tc.tree, tc.inProcess, got, tc.want)
		}
	}
}

func TestRunTreeProbeReportsMissingServer(t *testing.T) {
	res := runTreeProbe(treeProbeDeps{
		lookPath: lookPathStub(map[string]string{"node": "/opt/homebrew/bin/node"}),
		runInServer: func(string, string) (string, error) {
			return "", fmt.Errorf("no tmux server running on leo's socket")
		},
	}, "tmux", "10.0.2.9:443")

	if res.State != "" {
		t.Fatalf("State = %q, want empty (no verdict)", res.State)
	}
	if !strings.Contains(res.Detail, "no tmux server") {
		t.Fatalf("Detail = %q, want the reason surfaced", res.Detail)
	}
}

func TestRunTreeProbeReportsMissingBinary(t *testing.T) {
	res := runTreeProbe(treeProbeDeps{
		lookPath:    lookPathStub(nil),
		runInServer: func(string, string) (string, error) { return "OK", nil },
	}, "tmux", "10.0.2.9:443")

	if res.State != "" {
		t.Fatalf("State = %q, want empty (no verdict)", res.State)
	}
	if !strings.Contains(res.Detail, "brew") {
		t.Fatalf("Detail = %q, want the install hint", res.Detail)
	}
}

func TestRunTreeProbeSuccess(t *testing.T) {
	var gotCmd string
	res := runTreeProbe(treeProbeDeps{
		lookPath: lookPathStub(map[string]string{"node": "/opt/homebrew/bin/node"}),
		runInServer: func(_ string, cmd string) (string, error) {
			gotCmd = cmd
			return "OK\n", nil
		},
	}, "tmux", "10.0.2.9:443")

	if res.State != "granted" {
		t.Fatalf("State = %q, want granted", res.State)
	}
	if res.Binary != "/opt/homebrew/bin/node" {
		t.Fatalf("Binary = %q, want the resolved node path", res.Binary)
	}
	if !strings.Contains(gotCmd, "10.0.2.9") {
		t.Fatalf("probe command %q missing target", gotCmd)
	}
	if res.Detail != "OK" {
		t.Fatalf("Detail = %q, want the trimmed probe output", res.Detail)
	}
}

func TestRunTreeProbeDenied(t *testing.T) {
	res := runTreeProbe(treeProbeDeps{
		lookPath:    lookPathStub(map[string]string{"node": "/opt/homebrew/bin/node"}),
		runInServer: func(string, string) (string, error) { return "FAIL EHOSTUNREACH\n", nil },
	}, "tmux", "10.0.2.9:443")

	if res.State != "denied" {
		t.Fatalf("State = %q, want denied", res.State)
	}
	if !strings.Contains(res.Detail, "EHOSTUNREACH") {
		t.Fatalf("Detail = %q, want the probe output", res.Detail)
	}
}
