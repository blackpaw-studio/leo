package service

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// TeardownSessions kills the tmux session of every agent recorded under
// homePath, by exact session name. It is the explicit half of a permanent
// stop (`leo service stop`, with or without --daemon): a daemon shutdown never
// kills sessions itself, so that restarts, handovers and crashes leave them
// running for the next daemon to adopt. Callers must have stopped the daemon
// first, or its supervisors would respawn what this kills.
//
// managedTmuxPath is the install marker's tmux_path ("" when unmanaged); logw
// receives the resolver's diagnostics and killSession's give-up warnings.
func TeardownSessions(homePath, managedTmuxPath string, logw io.Writer) error {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading agents: %w", err)
	}
	if len(records) == 0 {
		return nil
	}
	tmuxPath, err := resolveDaemonTmux(managedTmuxPath, logw)
	if err != nil {
		return fmt.Errorf("resolving tmux: %w", err)
	}
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	prev := stopWarnLog
	stopWarnLog = logw
	defer func() { stopWarnLog = prev }()
	for _, name := range names {
		killSession(tmuxPath, agent.SessionName(name), name)
	}
	return nil
}
