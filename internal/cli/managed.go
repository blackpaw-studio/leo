package cli

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/managed"
)

// Seams: loadManaged reads the marker; managedWarnOut receives the one-time
// warning about an invalid marker.
var (
	loadManaged = func(stateDir string) managed.State {
		return managed.Load(stateDir, managed.DefaultDeps())
	}
	managedWarnOut  io.Writer = os.Stderr
	managedWarnOnce sync.Once
)

// managedState loads the managed-install marker for cfg's home. An invalid
// marker is reported on stderr once per process and treated as unmanaged.
func managedState(cfg *config.Config) managed.State {
	st := loadManaged(cfg.StatePath())
	if st.Warning != "" {
		managedWarnOnce.Do(func() { fmt.Fprintf(managedWarnOut, "warning: %s\n", st.Warning) })
	}
	return st
}

// selfExecutable resolves leo's own persisted path: the marker's self_path
// when managed, otherwise os.Executable.
func selfExecutable(cfg *config.Config) func() (string, error) {
	return managed.SelfExecutable(managedState(cfg), os.Executable)
}
