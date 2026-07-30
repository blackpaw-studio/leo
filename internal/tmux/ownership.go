// Ownership tracking for leo's foreground tmux server.
//
// EnsureForegroundServer adopts any server carrying the @leo-foreground
// marker, and that marker is sticky for the server's whole life — so a server
// leo started eight days and a dozen daemon restarts ago is indistinguishable
// from one started 200ms ago. Adoption is deliberate (it keeps agent sessions
// alive across `leo update`), but it silently voids the invariant the
// foreground server exists to uphold: that a LIVE signed leo process is the
// responsible process for every agent pane.
//
// These helpers make the difference observable. StartForegroundServer stamps
// the creating daemon's identity on the server; ServerOwnership reads it back
// and reports whether that process is still alive. Identity is (pid, start
// time), not pid alone: pid reuse on a long-uptime machine is the leading
// hypothesis for how the Local Network grant lapses in the first place, and a
// recycled pid must not read as "still my child".
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ownerMarkerKey is the tmux global option holding "<pid> <start time>" for
// the leo process that created the foreground server.
const ownerMarkerKey = "@leo-foreground-owner"

// psExecCommand is the seam tests replace for process-identity lookups.
var psExecCommand = exec.Command

// Ownership describes who created the tmux server currently on leo's socket.
type Ownership struct {
	// ServerPID is the tmux server's own pid (0 if it couldn't be read).
	ServerPID int
	// Stamped reports whether the server carries a usable ownership stamp.
	// False for a server created before stamping existed, or one whose stamp
	// is malformed.
	Stamped bool
	// OwnerPID is the pid of the leo process that created the server.
	OwnerPID int
	// OwnerLive reports whether that exact process is still running — pid
	// alive AND start time matching the stamp.
	OwnerLive bool
}

// processStartTime returns pid's start time as reported by `ps -o lstart=`,
// with runs of whitespace collapsed. ps pads single-digit days ("Jul  9"), and
// an unnormalized comparison would make every owner look recycled.
func processStartTime(pid int) (string, error) {
	out, err := psExecCommand("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", fmt.Errorf("reading start time of pid %d: %w", pid, err)
	}
	normalized := strings.Join(strings.Fields(string(out)), " ")
	if normalized == "" {
		return "", fmt.Errorf("no start time reported for pid %d", pid)
	}
	return normalized, nil
}

// stampOwner records pid's identity on the running server.
func stampOwner(tmuxPath string, pid int) error {
	start, err := processStartTime(pid)
	if err != nil {
		return err
	}
	value := fmt.Sprintf("%d %s", pid, start)
	out, err := serverExecCommand(tmuxPath, Args("set", "-g", ownerMarkerKey, value)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stamping tmux server owner: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ServerOwnership reads the ownership stamp off the running server. It errors
// only when there is no server to ask; a missing or malformed stamp is
// reported as Stamped=false, since "unknown" is a legitimate answer here and
// must not be conflated with "the owner is gone".
func ServerOwnership(tmuxPath string) (Ownership, error) {
	out, err := serverExecCommand(tmuxPath, Args("show-options", "-g")...).Output()
	if err != nil {
		return Ownership{}, fmt.Errorf("reading tmux global options: %w", err)
	}

	own := Ownership{ServerPID: serverPID(tmuxPath)}

	value, found := optionValue(string(out), ownerMarkerKey)
	if !found {
		return own, nil
	}

	pidField, start, ok := strings.Cut(value, " ")
	if !ok {
		return own, nil
	}
	pid, convErr := strconv.Atoi(pidField)
	if convErr != nil {
		return own, nil
	}

	own.Stamped = true
	own.OwnerPID = pid

	actual, startErr := processStartTime(pid)
	own.OwnerLive = startErr == nil && actual == strings.Join(strings.Fields(start), " ")
	return own, nil
}

// optionValue pulls a single global option's value out of a `show-options -g`
// dump, unquoting it. tmux quotes any value containing spaces, which every
// ownership stamp does.
//
// The key must match the whole first token, not merely a prefix:
// "@leo-foreground-owner" is a superstring of "@leo-foreground", and a prefix
// match would let an owner stamp masquerade as the foreground marker.
func optionValue(dump, key string) (string, bool) {
	for _, line := range strings.Split(dump, "\n") {
		name, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		if name != key {
			continue
		}
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted, true
		}
		return value, true
	}
	return "", false
}

// serverPID reads the tmux server's own pid, returning 0 when unavailable —
// it's for reporting only, so a failure here must not fail the caller.
func serverPID(tmuxPath string) int {
	out, err := serverExecCommand(tmuxPath, Args("display-message", "-p", "#{pid}")...).Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return pid
}
