package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
	"github.com/spf13/cobra"
)

// bridgeAgentEnv names the variable leo sets at launch to the bridge key.
const bridgeAgentEnv = "LEO_BRIDGE_AGENT"

const (
	// bridgeReportTimeout bounds one report, so a wedged daemon cannot stall
	// the mod's report chain for long.
	bridgeReportTimeout = 10 * time.Second
	// bridgeParentPollInterval is how often an idle `leo bridge` checks that
	// the claude process that spawned it is still there.
	bridgeParentPollInterval = 2 * time.Second
)

// bridgeDeps are the effects of `leo bridge`, injected so the command can be
// driven without a daemon or a parent process.
type bridgeDeps struct {
	openStream func(ctx context.Context, home, agent string) (io.ReadCloser, error)
	postReport func(ctx context.Context, home, agent string, body []byte) error
	getenv     func(string) string
	// homeDir resolves the leo home whose daemon socket to use.
	homeDir func() string
	// parentGone is closed once the process that spawned us has exited.
	parentGone func(ctx context.Context) <-chan struct{}
}

func defaultBridgeDeps() bridgeDeps {
	return bridgeDeps{
		openStream: daemon.OpenBridgeStream,
		postReport: daemon.PostBridgeReport,
		getenv:     os.Getenv,
		homeDir:    func() string { return bridgeHomeDir(cfgFile, os.Getenv) },
		parentGone: watchParent,
	}
}

func newBridgeCmd() *cobra.Command { return newBridgeCmdWith(defaultBridgeDeps()) }

// newBridgeCmdWith builds `leo bridge`, the claude mod's link to the daemon:
// bare, it streams the agent's commands to stdout as JSON lines; `report`
// posts one ack/hello/event, read from stdin, back.
func newBridgeCmdWith(deps bridgeDeps) *cobra.Command {
	var agentFlag string
	cmd := &cobra.Command{
		Use:    "bridge",
		Hidden: true,
		Short:  "Claude mod bridge plumbing (internal)",
		Long: `Streams an agent's bridge commands from the leo daemon to stdout, one JSON
object per line, until the daemon ends the stream. Spawned by the leo-bridge
Claude Code mod; not for interactive use.`,
		Args: cobra.NoArgs,
		// stdout is the mod's command pipe: nothing but command lines may
		// reach it, so never let cobra print usage or errors there.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agent, err := resolveBridgeAgent(agentFlag, deps.getenv)
			if err != nil {
				return err
			}
			return runBridgeStream(cmd.Context(), deps, agent, cmd.OutOrStdout())
		},
	}
	cmd.PersistentFlags().StringVar(&agentFlag, "agent", "", "bridge key (default $LEO_BRIDGE_AGENT)")
	cmd.AddCommand(newBridgeReportCmd(deps, &agentFlag))
	return cmd
}

func newBridgeReportCmd(deps bridgeDeps, agentFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:    "report",
		Hidden: true,
		Short:  "Post one bridge report (ack, hello or event), read from stdin, to the leo daemon",
		Long: `Reads one JSON report from stdin and posts it to the leo daemon. The report
travels on stdin because it can carry a whole prompt or final message, far
more than argv holds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agent, err := resolveBridgeAgent(*agentFlag, deps.getenv)
			if err != nil {
				return err
			}
			body, err := readBridgeReport(cmd.InOrStdin())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), bridgeReportTimeout)
			defer cancel()
			err = deps.postReport(ctx, deps.homeDir(), agent, body)
			if errors.Is(err, daemon.ErrBridgeGone) {
				// The launch this report belongs to is over: it is moot,
				// and failing would only have the mod retry it.
				fmt.Fprintf(cmd.ErrOrStderr(), "bridge report: dropped: %v\n", err)
				return nil
			}
			return err
		},
	}
}

// readBridgeReport reads the whole report from r, refusing an empty one and
// anything past the daemon's limit rather than posting it to be rejected.
func readBridgeReport(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, daemon.MaxBridgeReportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("bridge report: reading stdin: %w", err)
	}
	if int64(len(body)) > daemon.MaxBridgeReportBytes {
		return nil, fmt.Errorf("bridge report: report exceeds %d bytes", daemon.MaxBridgeReportBytes)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("bridge report: no report on stdin")
	}
	return body, nil
}

// resolveBridgeAgent picks --agent, else $LEO_BRIDGE_AGENT (the key leo
// routes this claude by), and validates it with the same rule the daemon
// applies. $LEO_PROCESS_NAME is deliberately not consulted: it names the
// process for display and differs from the key for dispatches.
func resolveBridgeAgent(flag string, getenv func(string) string) (string, error) {
	agent := flag
	if agent == "" {
		agent = getenv(bridgeAgentEnv)
	}
	if agent == "" {
		return "", errors.New("bridge: no agent: pass --agent or set " + bridgeAgentEnv)
	}
	if !config.ValidName(agent) {
		return "", fmt.Errorf("bridge: invalid agent name %q", agent)
	}
	return agent, nil
}

// runBridgeStream copies agent's command stream to out. A clean end of
// stream exits 0, as does hanging up on a vanished parent; a failed connect,
// read or write is an error.
func runBridgeStream(ctx context.Context, deps bridgeDeps, agent string, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := deps.openStream(ctx, deps.homeDir(), agent)
	if err != nil {
		return fmt.Errorf("bridge: %w", err)
	}
	defer body.Close()

	// An orphaned bridge would keep the agent's stream registered after its
	// claude is gone, so hang up when the parent disappears.
	parentGone := deps.parentGone(ctx)
	hungUp := make(chan struct{})
	go func() {
		select {
		case <-parentGone:
			close(hungUp)
			cancel()
			_ = body.Close()
		case <-ctx.Done():
		}
	}()

	err = copyBridgeLines(body, out)
	select {
	case <-hungUp:
		return nil
	default:
		return err
	}
}

// copyBridgeLines writes each complete line of r to out in a single write,
// flushing out after each when it buffers. A trailing partial line means the
// stream was cut mid-command; it is dropped, never passed on.
func copyBridgeLines(r io.Reader, out io.Writer) error {
	type flusher interface{ Flush() error }
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) > 0 {
					return fmt.Errorf("bridge: stream ended mid-command (%d bytes dropped)", len(line))
				}
				return nil
			}
			return fmt.Errorf("bridge: reading stream: %w", err)
		}
		if _, err := out.Write(line); err != nil {
			return fmt.Errorf("bridge: writing command: %w", err)
		}
		if f, ok := out.(flusher); ok {
			if err := f.Flush(); err != nil {
				return fmt.Errorf("bridge: flushing command: %w", err)
			}
		}
	}
}

// bridgeHomeDir resolves the leo home whose socket `leo bridge` talks to:
// the --config file's directory, else $LEO_BRIDGE_HOME (the home of the
// daemon that launched this claude), else $LEO_HOME, else the default home.
func bridgeHomeDir(cfgPath string, getenv func(string) string) string {
	if cfgPath != "" {
		if abs, err := filepath.Abs(cfgPath); err == nil {
			return filepath.Dir(abs)
		}
		return filepath.Dir(cfgPath)
	}
	if home := getenv(bridgemod.EnvHome); home != "" {
		return home
	}
	if home := getenv("LEO_HOME"); home != "" {
		return home
	}
	return config.DefaultHome()
}

// watchParent closes the returned channel once this process is re-parented,
// which is how an exited parent shows up on unix.
func watchParent(ctx context.Context) <-chan struct{} {
	gone := make(chan struct{})
	parent := os.Getppid()
	go func() {
		ticker := time.NewTicker(bridgeParentPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if os.Getppid() != parent {
					close(gone)
					return
				}
			}
		}
	}()
	return gone
}
