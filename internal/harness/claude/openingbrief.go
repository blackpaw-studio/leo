package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ArgvPromptLimit is a sanity cap on an opening prompt's size, measured on
// the raw bytes written to its brief file. The prompt itself never appears
// as literal launch argv text (see BriefArgvWord), sidestepping tmux 3.6a's
// own ~16 KiB total client-command limit (measured: a 15 KiB literal
// argument to `tmux new-session -d "<cmd>"` launched, a 17 KiB one failed
// with "command too long") — but the $(cat ...) substitution's *result*
// still becomes a single argv string in the exec'd claude process, and Linux
// caps any one argv/envp string at MAX_ARG_STRLEN, 32 pages (128 KiB on the
// common 4 KiB page size), independent of the much larger overall ARG_MAX. A
// prompt between this cap and the old 256 KiB would launch fine on macOS but
// fail with E2BIG on Linux. 96 KiB stays safely under MAX_ARG_STRLEN with
// headroom for the shell's own overhead in materializing the substitution;
// callers above it must fall back to some other delivery mechanism (e.g. a
// tmux paste/inject) or reject the prompt outright.
const ArgvPromptLimit = 96 * 1024

// DeliversPromptViaArgv reports whether an opening prompt of this size can be
// delivered via claude's launch-time argv (as a $(cat <brief file>) command
// substitution — see BriefArgvWord) rather than some other mechanism. Callers
// are responsible for confirming the target harness is actually claude first;
// this only judges size.
func DeliversPromptViaArgv(prompt string) bool {
	return len(prompt) <= ArgvPromptLimit
}

// BriefArgvWord is the shell text to place in claude's launch-argv positional
// slot for an opening prompt delivered via brief file. It expands to path's
// full contents via command substitution instead of embedding them directly,
// so the tmux client command that carries the launch stays a few dozen bytes
// regardless of the prompt's actual size (see ArgvPromptLimit).
//
// The returned text is NOT safe to pass through a generic per-argument
// shell-quote pass (single quotes would disable the "$(...)" expansion
// entirely): callers must place it in the final shell command verbatim,
// after quoting every other argument. The outer double quotes keep the
// substituted content as one argv word even when it contains spaces or
// newlines; the inner quoting of path keeps a path containing a quote or a
// space safe.
func BriefArgvWord(path string) string {
	return `"$(cat ` + quotePathSingle(path) + `)"`
}

// quotePathSingle wraps s in single quotes with proper escaping, matching
// the shell-quoting convention used across leo's tmux command assembly
// (internal/service/process.go's shellQuote, internal/consult/viewer.go's
// shellQuote). Duplicated here (rather than imported) to keep this package
// free of a dependency on either.
func quotePathSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// openingBriefPath computes the path of a subdir-scoped opening-brief file
// under a leo home's state dir: <home>/state/<subdir>/<id>.txt. Returns "" if
// any input is unusable (empty, or id looks like a path component that could
// escape subdir).
func openingBriefPath(homePath, subdir, id string) string {
	if homePath == "" || id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return ""
	}
	return filepath.Join(homePath, "state", subdir, id+".txt")
}

// DispatchBriefSpillDir names the directory (under a leo home's state dir)
// holding per-dispatch opening-brief files.
const DispatchBriefSpillDir = "dispatch-briefs"

// DispatchBriefPath is dispatch id's opening-brief file path ("" if
// unusable).
func DispatchBriefPath(homePath, id string) string {
	return openingBriefPath(homePath, DispatchBriefSpillDir, id)
}

// AgentBriefSpillDir names the directory holding per-ephemeral-agent
// opening-prompt brief files.
const AgentBriefSpillDir = "agent-briefs"

// AgentBriefPath is an ephemeral agent's opening-prompt brief file path (""
// if unusable). Deterministic from the agent's name, so cleanup (on stop or
// delete) never needs to persist the path separately.
func AgentBriefPath(homePath, agentName string) string {
	return openingBriefPath(homePath, AgentBriefSpillDir, agentName)
}

// WritePrivateBrief writes brief to path, creating its directory as needed.
// The brief can carry orchestrator- or user-authored task instructions, so
// both the directory and the file are owner-only.
func WritePrivateBrief(path, brief string) error {
	if path == "" {
		return errors.New("no opening-brief path available")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating opening-brief directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone (a prior leo
	// version, or something else entirely, could have created it looser);
	// tighten it explicitly rather than trusting whatever created it first.
	// #nosec G302 -- a directory needs the owner execute bit; 0700 is owner-only
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("securing opening-brief directory: %w", err)
	}
	// A stale file (leftover from a crashed prior run) or a symlink planted
	// at this exact path must never be reused or followed: os.WriteFile
	// would keep a stale file's existing mode, and would happily write
	// through a symlink to wherever it points. Remove whatever is there
	// (Remove itself never follows a symlink) and create fresh with
	// O_EXCL|O_NOFOLLOW, so even something recreated in the gap between the
	// Remove and this Open is refused rather than silently written to.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale opening-brief file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("creating opening-brief file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte(brief)); err != nil {
		return fmt.Errorf("writing opening brief: %w", err)
	}
	return nil
}

// RemoveBrief deletes path, tolerating one that is already gone. path=="" is
// a no-op (matches the "unusable path" contract of the *BriefPath helpers).
func RemoveBrief(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing opening-brief file: %w", err)
	}
	return nil
}
