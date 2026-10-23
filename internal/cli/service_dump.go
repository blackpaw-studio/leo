package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

const (
	// serviceDumpTimeout is how long to wait for the daemon to write its dump.
	serviceDumpTimeout = 10 * time.Second
	// serviceDumpPoll paces the wait for the new dump file.
	serviceDumpPoll = 100 * time.Millisecond
)

// serviceDumpDeps are the side effects runServiceDump drives.
type serviceDumpDeps struct {
	target  func(ctx context.Context, home string) (daemon.DumpInfo, bool)
	signal  func(pid int) error
	glob    func(pattern string) ([]string, error)
	now     func() time.Time
	sleep   func(time.Duration)
	timeout time.Duration
}

func defaultServiceDumpDeps() serviceDumpDeps {
	return serviceDumpDeps{
		target:  daemon.DumpTarget,
		signal:  func(pid int) error { return syscall.Kill(pid, syscall.SIGUSR1) },
		glob:    filepath.Glob,
		now:     time.Now,
		sleep:   time.Sleep,
		timeout: serviceDumpTimeout,
	}
}

func newServiceDumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dump",
		Short: "Write the running daemon's goroutine stacks to a file",
		Long: `Ask the running daemon to write every goroutine's stack to
<home>/state/goroutines-<unix>.txt and print that path. The daemon keeps
serving; nothing about its state changes. Use it on a daemon that is slow
to start or seems stuck, to see where it is.

The daemon's PID comes from its own /health answer, and only a daemon that
advertises the dump handler is signalled: an older daemon would be
terminated by the signal, so it is refused instead.`,
		Example: `  leo service dump`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runServiceDump(cmd.Context(), cfg.HomePath, cmd.OutOrStdout(), defaultServiceDumpDeps())
		},
	}
}

// runServiceDump signals the daemon for home and prints the path of the dump
// it writes.
func runServiceDump(ctx context.Context, home string, out io.Writer, deps serviceDumpDeps) error {
	info, ok := deps.target(ctx, home)
	if !ok {
		return fmt.Errorf("daemon is not running (no answer on %s)", daemon.SockPath(home))
	}
	if !info.Capable {
		return fmt.Errorf("daemon pid %d (%s) predates 'leo service dump'; not signalled (SIGUSR1 would terminate it)", info.PID, info.Version)
	}

	pattern := filepath.Join(home, "state", "goroutines-*.txt")
	before, err := deps.glob(pattern)
	if err != nil {
		return fmt.Errorf("listing existing dumps: %w", err)
	}
	if err := deps.signal(info.PID); err != nil {
		return fmt.Errorf("signalling daemon pid %d: %w", info.PID, err)
	}

	deadline := deps.now().Add(deps.timeout)
	for {
		after, err := deps.glob(pattern)
		if err != nil {
			return fmt.Errorf("listing dumps: %w", err)
		}
		if path, found := firstNew(before, after); found {
			_, err := fmt.Fprintln(out, path)
			return err
		}
		if !deps.now().Before(deadline) {
			return fmt.Errorf("signalled pid %d but no dump appeared in %s; see 'leo service logs'", info.PID, deps.timeout)
		}
		deps.sleep(serviceDumpPoll)
	}
}

// firstNew returns the first path in after that is not in before.
func firstNew(before, after []string) (string, bool) {
	seen := make(map[string]bool, len(before))
	for _, p := range before {
		seen[p] = true
	}
	for _, p := range after {
		if !seen[p] {
			return p, true
		}
	}
	return "", false
}
