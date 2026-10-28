// Package remote builds the ssh argv every host-targeted call shares, so the
// CLI's `--host` dispatch and the daemon's agent moves multiplex over one
// ControlMaster connection per host.
package remote

import (
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
)

// ControlOpts returns the OpenSSH ControlMaster options shared by every
// host-targeted SSH call so they multiplex over one connection: the persistent
// `leo host forward` master, each `agent` dispatch, the `--cc` cell stream and
// agent moves.
//
// ControlMaster=auto reuses an existing master at res.ControlPath when one is
// live (e.g. the forward holds it) and otherwise opens its own short-lived
// connection — no ControlPersist here, so non-forward callers never leave a
// lingering master behind. Returns nil for localhost (empty ControlPath).
//
// The control socket's parent directory must exist before ssh can bind there;
// we create it best-effort and, if that fails, return nil so the call still
// connects (just without multiplexing) rather than erroring.
func ControlOpts(res config.HostResolution) []string {
	if res.ControlPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(res.ControlPath), 0o700); err != nil {
		return nil
	}
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + res.ControlPath,
	}
}

// SSHArgv returns a fresh ssh argv (excluding "ssh") for res: the host, its
// ssh_args, the ControlMaster options, then tail. batch prepends
// `-o BatchMode=yes`, so a call that cannot prompt fails instead of hanging.
// The slice never aliases the config-loaded SSHArgs backing array.
func SSHArgv(res config.HostResolution, batch bool, tail ...string) []string {
	control := ControlOpts(res)
	args := make([]string, 0, 3+len(res.Host.SSHArgs)+len(control)+len(tail))
	if batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args, res.Host.SSH)
	args = append(args, res.Host.SSHArgs...)
	args = append(args, control...)
	args = append(args, tail...)
	return args
}
