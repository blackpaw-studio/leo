package cli

import (
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
)

// sshControlOpts returns the OpenSSH ControlMaster options shared by every
// host-targeted SSH call so they multiplex over one connection: the persistent
// `leo host forward` master, each `agent` dispatch, and the `--cc` cell stream.
//
// ControlMaster=auto reuses an existing master at res.ControlPath when one is
// live (e.g. the forward holds it) and otherwise opens its own short-lived
// connection — no ControlPersist here, so non-forward callers never leave a
// lingering master behind. Returns nil for localhost (empty ControlPath).
//
// The control socket's parent directory must exist before ssh can bind there;
// we create it best-effort and, if that fails, return nil so the call still
// connects (just without multiplexing) rather than erroring.
func sshControlOpts(res config.HostResolution) []string {
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
