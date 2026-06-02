package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// safeTerms enumerates TERM values that ship in the historic ncurses
// distribution and are universally available on Unix hosts. SSHing into a
// remote with one of these set requires no terminfo bootstrapping.
var safeTerms = map[string]bool{
	"xterm":           true,
	"xterm-color":     true,
	"xterm-256color":  true,
	"screen":          true,
	"screen-256color": true,
	"tmux":            true,
	"tmux-256color":   true,
	"vt100":           true,
	"vt220":           true,
	"linux":           true,
	"ansi":            true,
	"dumb":            true,
}

// terminfoFallback is the TERM we drop to on the remote command when we can
// not ship the local TERM's entry. xterm-256color is the widest-supported
// baseline that still gives tmux usable color and key handling.
const terminfoFallback = "xterm-256color"

// terminfoCacheDir resolves the directory we drop per-host sentinel files
// into. Indirected so tests can sandbox into t.TempDir() without touching the
// developer's real ~/.leo.
var terminfoCacheDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "leo-terminfo")
	}
	return filepath.Join(home, ".leo", "state", "terminfo")
}

// terminfoStderr is where install diagnostics get written. Indirected so
// tests can suppress noise; production points at agentStderr.
var terminfoStderr = func() *os.File { return os.Stderr }

// terminfoInfocmp builds the `infocmp -x <term>` command. Test seam so we
// can simulate a missing infocmp without messing with $PATH.
var terminfoInfocmp = func(term string) *exec.Cmd {
	path, err := exec.LookPath("infocmp")
	if err != nil {
		return nil
	}
	return exec.Command(path, "-x", term)
}

// ensureRemoteTerminfo makes the local $TERM usable on the remote referenced
// by res. When TERM is already in safeTerms or a prior install left a
// sentinel for (host, TERM), it returns "" — the SSH command can pass TERM
// through untouched. When the install attempt fails (no infocmp locally, no
// tic remotely, etc.), it returns terminfoFallback so callers can prepend
// `env TERM=<fallback>` to the remote argv and at least let tmux start.
//
// Called from leo's SSH-attach paths. Cheap on the hot path: a single
// os.Stat after the first successful install per (host, TERM) pair.
func ensureRemoteTerminfo(res config.HostResolution) string {
	if res.Localhost {
		return ""
	}
	term := strings.TrimSpace(os.Getenv("TERM"))
	if term == "" || safeTerms[term] {
		return ""
	}
	cacheDir := terminfoCacheDir()
	sentinel := terminfoSentinelPath(cacheDir, res.Host.SSH, term)
	if _, err := os.Stat(sentinel); err == nil {
		return ""
	}
	if err := installRemoteTerminfo(res, term); err != nil {
		fmt.Fprintf(terminfoStderr(), "leo: could not install terminfo %q on %s (%v); falling back to TERM=%s\n", term, res.Host.SSH, err, terminfoFallback)
		return terminfoFallback
	}
	if err := os.MkdirAll(cacheDir, 0o750); err == nil {
		_ = os.WriteFile(sentinel, []byte(term+"\n"), 0o644)
	}
	return ""
}

// installRemoteTerminfo runs `infocmp -x <term>` locally and pipes the
// compiled-source output into `tic -x -` on the remote over SSH. Both tools
// ship with ncurses; if either is missing or fails we return an error and
// let the caller downgrade TERM.
func installRemoteTerminfo(res config.HostResolution, term string) error {
	infocmp := terminfoInfocmp(term)
	if infocmp == nil {
		return fmt.Errorf("infocmp not found locally — install ncurses")
	}
	var sourceBuf, infocmpErr bytes.Buffer
	infocmp.Stdout = &sourceBuf
	infocmp.Stderr = &infocmpErr
	if err := infocmp.Run(); err != nil {
		msg := strings.TrimSpace(infocmpErr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("infocmp -x %s: %s", term, msg)
	}
	if sourceBuf.Len() == 0 {
		return fmt.Errorf("infocmp -x %s produced no output", term)
	}

	sshArgs := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, "tic", "-x", "-")
	tic := agentExecCommand("ssh", sshArgs...)
	tic.Stdin = &sourceBuf
	var ticErr bytes.Buffer
	tic.Stderr = &ticErr
	if err := tic.Run(); err != nil {
		msg := strings.TrimSpace(ticErr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ssh %s tic -x -: %s", res.Host.SSH, msg)
	}
	return nil
}

// terminfoSentinelPath derives a per-host filename inside cacheDir. We hash
// the SSH target so an entry like "user@host:2222" stays a flat filename,
// and append the literal TERM for readability and debuggability.
func terminfoSentinelPath(cacheDir, host, term string) string {
	sum := sha256.Sum256([]byte(host))
	return filepath.Join(cacheDir, hex.EncodeToString(sum[:6])+"-"+term)
}

// applyRemoteTermFallback returns sshArgs with `env TERM=<fallback>` spliced
// in just before the remote command, when override is non-empty. The result
// keeps the SSH options at the front (everything up to and including the
// SSH target + per-host extras) and prepends the env shim to whatever the
// caller intends to run on the remote.
//
// prefixLen is the count of leading entries in sshArgs that are SSH plumbing
// (e.g. "-t", "<user@host>", "-p", "2222"). Indexes >= prefixLen are
// considered remote command argv.
func applyRemoteTermFallback(sshArgs []string, prefixLen int, override string) []string {
	if override == "" {
		return sshArgs
	}
	out := make([]string, 0, len(sshArgs)+2)
	out = append(out, sshArgs[:prefixLen]...)
	out = append(out, "env", "TERM="+override)
	out = append(out, sshArgs[prefixLen:]...)
	return out
}

// ensureRemoteTerminfoFn is a package-level testability seam. Tests stub it
// to a no-op so the existing ssh-arg expectations don't have to account for
// the bootstrap pass.
var ensureRemoteTerminfoFn = ensureRemoteTerminfo
