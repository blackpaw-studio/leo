// In-tree Local Network probing for `leo doctor`.
//
// macOS attributes a network connection to the *responsible* process, not
// necessarily the one calling connect(2). leo's tmux server is started as a
// child of the signed leo binary precisely so agent panes inherit leo's Local
// Network grant (see internal/tmux/server.go). That inheritance can lapse —
// e.g. once the daemon that created the server has exited — and when it does,
// third-party binaries under agent sessions are silently denied.
//
// A probe run from the leo CLI process cannot see any of that: leo's own
// binary holds the grant outright, so it always succeeds. Discriminating
// requires two things at once — run inside the tmux tree, and run a
// THIRD-PARTY binary (Apple platform binaries are exempt from Local Network
// TCC and would report a false pass).
package cli

import (
	"fmt"
	"net"
	"strings"
)

// treeDeniedState is the verdict for the specific, actionable condition this
// probe exists to find: the tmux tree is denied while leo's own process is
// allowed.
const treeDeniedState = "denied (tmux tree)"

// treeProbeCandidates are tried in order. All must be non-Apple binaries to be
// usable — see isPlatformBinary.
//
// curl is deliberately absent. It reports its OWN exit codes rather than
// errno, and 7 ("couldn't connect") covers refused, unreachable, and timed-out
// alike — so it cannot distinguish "the packet left the machine" from "the OS
// blocked it", which is the entire basis of the verdict. A probe that cannot
// discriminate is worse than no probe, because it produces a confident wrong
// answer.
var treeProbeCandidates = []string{"node", "python3"}

// platformBinaryDirs are the directories holding Apple platform binaries,
// which are exempt from Local Network TCC.
var platformBinaryDirs = []string{"/usr/bin/", "/bin/", "/usr/sbin/", "/sbin/", "/System/"}

// probeBinary is a third-party binary usable for an in-tree probe.
type probeBinary struct {
	Name string
	Path string
}

// treeProbeResult is the outcome of an in-tree probe. An empty State means no
// verdict was reached (no server, no usable binary, probe error); Detail then
// explains why.
type treeProbeResult struct {
	State  string
	Detail string
	Binary string
}

// treeProbeDeps injects the two things runTreeProbe touches outside itself, so
// tests can drive every branch without a tmux server or a real LAN.
type treeProbeDeps struct {
	lookPath    func(string) (string, error)
	runInServer func(tmuxPath, shellCmd string) (string, error)
}

// isPlatformBinary reports whether path is an Apple platform binary. Those are
// exempt from Local Network TCC, so probing with one proves nothing.
func isPlatformBinary(path string) bool {
	for _, dir := range platformBinaryDirs {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	return false
}

// selectTreeProbeBinary returns the first candidate that resolves to a
// non-platform binary.
func selectTreeProbeBinary(lookPath func(string) (string, error)) (probeBinary, error) {
	for _, name := range treeProbeCandidates {
		path, err := lookPath(name)
		if err != nil || isPlatformBinary(path) {
			continue
		}
		return probeBinary{Name: name, Path: path}, nil
	}
	return probeBinary{}, fmt.Errorf("no third-party probe binary found (install one with 'brew install node')")
}

// treeProbeCommand builds a shell command that connects to target and prints
// exactly "OK" or "FAIL <code>". Each variant uses the resolved absolute path:
// the tmux server's PATH is inherited from whichever process started it and
// cannot be assumed to include Homebrew.
func treeProbeCommand(b probeBinary, target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", fmt.Errorf("parsing probe target %q: %w", target, err)
	}

	switch b.Name {
	case "node":
		return fmt.Sprintf(
			`%s -e "const s=require('net').connect({host:'%s',port:%s},()=>{console.log('OK');process.exit(0)});`+
				`s.setTimeout(3000,()=>{console.log('FAIL ETIMEDOUT');process.exit(0)});`+
				`s.on('error',e=>{console.log('FAIL '+e.code);process.exit(0)})"`,
			b.Path, host, port), nil
	case "python3":
		// connect_ex returns an errno instead of raising, which keeps this to
		// a single line (python -c cannot hold a try/except without newlines).
		return fmt.Sprintf(
			`%s -c "import socket;s=socket.socket();s.settimeout(3);r=s.connect_ex(('%s',%s));`+
				`print('OK' if r==0 else 'FAIL '+str(r))"`,
			b.Path, host, port), nil
	default:
		return "", fmt.Errorf("no probe command for %q", b.Name)
	}
}

// classifyTreeOutput maps a probe command's stdout to a Local Network verdict.
//
// It follows classifyDial's core reasoning — an unreachable host means the OS
// dropped the packet before it left the machine (denied), while a refused
// connection proves the packet arrived (granted) — but recognizes two denial
// signatures classifyDial does not: ENETUNREACH and EPERM. Those show up here
// and not there because the probe runs in a *denied* process rather than a
// granted one, and the sandbox surfaces the block differently depending on
// which layer refuses first.
func classifyTreeOutput(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "OK" {
		return "granted"
	}
	switch {
	case containsAny(trimmed, "EHOSTUNREACH", "FAIL 65", "ENETUNREACH", "FAIL 51", "EPERM", "FAIL 1"):
		return "denied"
	case containsAny(trimmed, "ECONNREFUSED", "FAIL 61"):
		return "granted"
	default:
		return "undetermined"
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// combineLocalNetworkStates folds the in-tree and in-process verdicts into the
// one leo reports. The tree decides — that is where agents actually run — and a
// tree denial paired with an in-process success is named specifically, because
// that combination *is* the attribution failure.
//
// The in-process result is only ever consulted when the tree produced NO
// verdict (empty: no server, no probe binary, probe error). A probe that ran
// but was inconclusive stays inconclusive: leo's own process always holds
// leo's grant, so deferring to it there would convert "we don't know about the
// tree" into a confident "granted" — precisely the false pass this exists to
// eliminate.
func combineLocalNetworkStates(tree, inProcess string) string {
	switch tree {
	case "":
		return inProcess
	case "granted":
		return "granted"
	case "denied":
		if inProcess == "granted" {
			return treeDeniedState
		}
		return "denied"
	default:
		return tree
	}
}

// runTreeProbe picks a probe binary, runs it inside leo's tmux server, and
// classifies the result. Every failure to reach a verdict is reported as an
// empty State with an explanatory Detail rather than a misleading verdict.
func runTreeProbe(deps treeProbeDeps, tmuxPath, target string) treeProbeResult {
	bin, err := selectTreeProbeBinary(deps.lookPath)
	if err != nil {
		return treeProbeResult{Detail: err.Error()}
	}

	cmd, err := treeProbeCommand(bin, target)
	if err != nil {
		return treeProbeResult{Detail: err.Error(), Binary: bin.Path}
	}

	out, err := deps.runInServer(tmuxPath, cmd)
	if err != nil {
		return treeProbeResult{Detail: err.Error(), Binary: bin.Path}
	}

	return treeProbeResult{
		State:  classifyTreeOutput(out),
		Detail: strings.TrimSpace(out),
		Binary: bin.Path,
	}
}
