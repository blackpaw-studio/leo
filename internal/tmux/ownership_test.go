package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ownerStub models a fake tmux server carrying (or missing) leo's ownership
// stamp, plus a fake `ps` whose answers the test controls per pid.
type ownerStub struct {
	running bool
	// ownerOption is the raw value stored under @leo-foreground-owner. Empty
	// means the option is absent.
	ownerOption string
	// psStart maps pid -> lstart output; a missing pid means `ps` fails, i.e.
	// the process is gone.
	psStart map[string]string
	// suppressMarker omits @leo-foreground from the dump, modelling a server
	// that carries an owner stamp but no foreground marker.
	suppressMarker bool
	calls          []string
}

func (s *ownerStub) exec(_ string, args ...string) *exec.Cmd {
	s.calls = append(s.calls, strings.Join(args, " "))
	sub := ""
	if len(args) >= 3 {
		sub = args[2]
	}
	switch sub {
	case "display-message":
		if s.running {
			return exec.Command("echo", "32086")
		}
		return exec.Command("false")
	case "show-options":
		if !s.running {
			return exec.Command("false")
		}
		dump := sampleGlobalOptionsDump
		if !s.suppressMarker {
			dump += "@leo-foreground 1\n"
		}
		if s.ownerOption != "" {
			dump += fmt.Sprintf("%s %q\n", ownerMarkerKey, s.ownerOption)
		}
		return exec.Command("printf", "%s", dump)
	case "set":
		// args is: -L <socket> set -g <key> <value>
		if len(args) >= 6 && args[4] == ownerMarkerKey {
			s.ownerOption = args[5]
		}
		return exec.Command("true")
	default:
		return exec.Command("true")
	}
}

func (s *ownerStub) ps(_ string, args ...string) *exec.Cmd {
	pid := args[len(args)-1]
	if start, ok := s.psStart[pid]; ok {
		return exec.Command("printf", "%s", start)
	}
	return exec.Command("false")
}

func withOwnerStub(t *testing.T, s *ownerStub) {
	t.Helper()
	origExec, origPS := serverExecCommand, psExecCommand
	serverExecCommand, psExecCommand = s.exec, s.ps
	t.Cleanup(func() { serverExecCommand, psExecCommand = origExec, origPS })
}

func TestServerOwnershipReportsLiveOwner(t *testing.T) {
	stub := &ownerStub{
		running:     true,
		ownerOption: "33666 Wed Jul 29 20:12:21 2026",
		psStart:     map[string]string{"33666": "Wed Jul 29 20:12:21 2026\n"},
	}
	withOwnerStub(t, stub)

	own, err := ServerOwnership("tmux")
	if err != nil {
		t.Fatalf("ServerOwnership: %v", err)
	}
	if !own.Stamped {
		t.Fatal("Stamped = false, want true")
	}
	if own.OwnerPID != 33666 {
		t.Fatalf("OwnerPID = %d, want 33666", own.OwnerPID)
	}
	if !own.OwnerLive {
		t.Fatal("OwnerLive = false, want true")
	}
	if own.ServerPID != 32086 {
		t.Fatalf("ServerPID = %d, want 32086", own.ServerPID)
	}
}

// The adopted-orphan case: the daemon that created the server has exited, so
// the process macOS attributed Local Network responsibility to is gone.
func TestServerOwnershipDetectsDeadOwner(t *testing.T) {
	stub := &ownerStub{
		running:     true,
		ownerOption: "58688 Wed Jul 29 19:21:39 2026",
		psStart:     map[string]string{}, // pid 58688 no longer exists
	}
	withOwnerStub(t, stub)

	own, err := ServerOwnership("tmux")
	if err != nil {
		t.Fatalf("ServerOwnership: %v", err)
	}
	if !own.Stamped || own.OwnerPID != 58688 {
		t.Fatalf("ownership = %+v, want the stamp parsed", own)
	}
	if own.OwnerLive {
		t.Fatal("OwnerLive = true, want false for an exited owner")
	}
}

// PID reuse is the leading hypothesis for how the grant lapses on a long-lived
// server, so a live pid alone must not count as the same process.
func TestServerOwnershipDetectsRecycledPID(t *testing.T) {
	stub := &ownerStub{
		running:     true,
		ownerOption: "58688 Tue Jul 21 12:14:58 2026",
		psStart:     map[string]string{"58688": "Wed Jul 29 20:30:01 2026\n"},
	}
	withOwnerStub(t, stub)

	own, err := ServerOwnership("tmux")
	if err != nil {
		t.Fatalf("ServerOwnership: %v", err)
	}
	if own.OwnerLive {
		t.Fatal("OwnerLive = true, want false when the pid's start time differs")
	}
}

// A server started before ownership stamping existed carries the foreground
// marker but no owner option. That is "unknown", not "broken".
func TestServerOwnershipUnstampedServer(t *testing.T) {
	stub := &ownerStub{running: true}
	withOwnerStub(t, stub)

	own, err := ServerOwnership("tmux")
	if err != nil {
		t.Fatalf("ServerOwnership: %v", err)
	}
	if own.Stamped {
		t.Fatal("Stamped = true, want false")
	}
	if own.OwnerLive {
		t.Fatal("OwnerLive = true, want false when unstamped")
	}
}

func TestServerOwnershipErrorsWhenNoServer(t *testing.T) {
	stub := &ownerStub{running: false}
	withOwnerStub(t, stub)

	if _, err := ServerOwnership("tmux"); err == nil {
		t.Fatal("expected an error when no server is running")
	}
}

func TestServerOwnershipToleratesMalformedStamp(t *testing.T) {
	stub := &ownerStub{running: true, ownerOption: "not-a-pid whatever"}
	withOwnerStub(t, stub)

	own, err := ServerOwnership("tmux")
	if err != nil {
		t.Fatalf("ServerOwnership should not fail on a malformed stamp: %v", err)
	}
	if own.Stamped || own.OwnerLive {
		t.Fatalf("ownership = %+v, want an unusable stamp treated as unstamped", own)
	}
}

// @leo-foreground-owner is a superstring of @leo-foreground, so a raw prefix
// match would let an owner stamp alone satisfy the foreground-marker probe —
// making EnsureForegroundServer ADOPT a legacy auto-daemonized server it should
// have recycled, silently losing the Local Network attribution the marker is
// supposed to guarantee.
func TestMarkerStateIgnoresOwnerStampAlone(t *testing.T) {
	stub := &ownerStub{running: true}
	withOwnerStub(t, stub)
	// Owner stamp present, foreground marker absent.
	stub.ownerOption = "33666 Wed Jul 29 20:12:21 2026"
	stub.suppressMarker = true

	marked, ok := markerState("tmux")
	if !ok {
		t.Fatal("probe should have succeeded")
	}
	if marked {
		t.Fatal("marked = true: an owner stamp must not satisfy the foreground-marker probe")
	}
}

func TestOptionValueRequiresExactKeyToken(t *testing.T) {
	dump := "status on\n@leo-foreground-owner \"33666 Wed Jul 29 20:12:21 2026\"\n"

	if _, found := optionValue(dump, "@leo-foreground"); found {
		t.Fatal("optionValue matched a longer key as a prefix")
	}
	value, found := optionValue(dump, "@leo-foreground-owner")
	if !found {
		t.Fatal("optionValue did not find the exact key")
	}
	if value != "33666 Wed Jul 29 20:12:21 2026" {
		t.Fatalf("value = %q, want the unquoted stamp", value)
	}
}

func TestStampOwnerRecordsPIDAndStartTime(t *testing.T) {
	pid := os.Getpid()
	stub := &ownerStub{
		running: true,
		psStart: map[string]string{fmt.Sprint(pid): "Wed Jul 29 20:12:21 2026\n"},
	}
	withOwnerStub(t, stub)

	if err := stampOwner("tmux", pid); err != nil {
		t.Fatalf("stampOwner: %v", err)
	}
	want := fmt.Sprintf("%d Wed Jul 29 20:12:21 2026", pid)
	if stub.ownerOption != want {
		t.Fatalf("stamped %q, want %q", stub.ownerOption, want)
	}
}

// ps output pads short day-of-month values; the stamp and the later comparison
// must agree despite that, or every owner would look recycled.
func TestProcessStartTimeNormalizesWhitespace(t *testing.T) {
	stub := &ownerStub{
		running: true,
		psStart: map[string]string{"42": "Thu Jul  9 08:04:01 2026\n"},
	}
	withOwnerStub(t, stub)

	got, err := processStartTime(42)
	if err != nil {
		t.Fatalf("processStartTime: %v", err)
	}
	if got != "Thu Jul 9 08:04:01 2026" {
		t.Fatalf("processStartTime = %q, want collapsed whitespace", got)
	}
}

// StartForegroundServer must not treat a failed stamp as fatal: the stamp is
// diagnostic metadata, and killing a working server over it would be worse
// than the bug it helps diagnose.
func TestStartForegroundServerSurvivesStampFailure(t *testing.T) {
	stub := &stubServer{}
	withStubServer(t, stub)
	// No psStart entry for our pid: processStartTime fails, so stampOwner does.
	origPS := psExecCommand
	psExecCommand = func(string, ...string) *exec.Cmd { return exec.Command("false") }
	t.Cleanup(func() { psExecCommand = origPS })

	if _, err := StartForegroundServer("tmux"); err != nil {
		t.Fatalf("StartForegroundServer should tolerate a stamp failure: %v", err)
	}
	if !stub.isRunning() {
		t.Fatal("server should still be running after a stamp failure")
	}
}
