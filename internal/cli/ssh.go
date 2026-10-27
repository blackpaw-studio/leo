package cli

import (
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/remote"
)

// sshControlOpts returns the OpenSSH ControlMaster options shared by every
// host-targeted SSH call; see remote.ControlOpts.
func sshControlOpts(res config.HostResolution) []string {
	return remote.ControlOpts(res)
}
