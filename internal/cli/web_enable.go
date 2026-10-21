package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

type webEnableOptions struct {
	bind            string
	allowHosts      []string
	gracefulOptions gracefulOptions
}

type webEnableState struct {
	Enabled bool   `json:"enabled"`
	Bind    string `json:"bind"`
	Port    int    `json:"port"`
}

// webEnableResult is `leo web enable --json`.
type webEnableResult struct {
	OK        bool           `json:"ok"`
	Changed   bool           `json:"changed"`
	Restarted bool           `json:"restarted"`
	Web       webEnableState `json:"web"`
	Warnings  []string       `json:"warnings,omitempty"`
}

type webEnableDeps struct {
	// daemonWeb asks the running daemon about its web listener; reachable is
	// false when no daemon answers.
	daemonWeb func(home string) (status daemon.WebStatus, reachable bool)
	// gracefulDeps builds the restart's drain and restart side effects.
	gracefulDeps func(cfg *config.Config) gracefulDeps
	save         func(cfg *config.Config) error
}

// runWebEnable turns the web listener on (and applies --bind/--allow-host),
// validating before it writes, then restarts a running daemon through the
// same routing as `leo service restart` so the listener comes up. Nothing
// changes, and nothing restarts, when the config already says so.
func runWebEnable(ctx context.Context, cfg *config.Config, opts webEnableOptions, deps webEnableDeps) (webEnableResult, error) {
	updated := *cfg
	updated.Web = cfg.Web
	updated.Web.AllowedHosts = slices.Clone(cfg.Web.AllowedHosts)
	updated.Web.Enabled = true
	if opts.bind != "" {
		updated.Web.Bind = opts.bind
	}
	for _, h := range opts.allowHosts {
		if !slices.Contains(updated.Web.AllowedHosts, h) {
			updated.Web.AllowedHosts = append(updated.Web.AllowedHosts, h)
		}
	}
	res := webEnableResult{Web: webStateOf(&updated)}

	if err := updated.Validate(); err != nil {
		return webEnableResult{}, failJSON("invalid_config", err)
	}
	configMatches := updated.Web.Enabled == cfg.Web.Enabled && updated.WebBind() == cfg.WebBind() && slices.Equal(updated.Web.AllowedHosts, cfg.Web.AllowedHosts)
	live, reachable := deps.daemonWeb(updated.HomePath)
	// A config that already matches is only a no-op while the daemon is
	// really serving it: a restart that failed after an earlier save leaves
	// the file right and the listener off.
	if configMatches && (!reachable || servesWeb(live, &updated)) {
		res.OK = true
		return res, nil
	}
	if !configMatches {
		if err := deps.save(&updated); err != nil {
			return webEnableResult{}, failJSON("write_failed", err)
		}
		res.Changed = true
	}
	if !reachable {
		res.OK = true
		return res, nil
	}
	_, err := restartJSON(ctx, &updated, managedState(&updated), opts.gracefulOptions, deps.gracefulDeps(&updated))
	var je *jsonError
	switch {
	case err == nil:
		res.Restarted = true
	case errors.As(err, &je) && je.Code == "not_registered":
		res.Warnings = append(res.Warnings, "the daemon is running outside launchd, so it was not restarted; restart it to apply the change")
	default:
		code, msg := "restart_failed", err.Error()
		if je != nil {
			code, msg = je.Code, je.Message
		}
		return res, &jsonError{Code: code, Message: msg, Payload: webEnableFailure{webEnableResult: res, Error: code, Message: msg}}
	}
	res.OK = true
	return res, nil
}

// servesWeb reports whether the daemon's listener is on cfg's address.
func servesWeb(live daemon.WebStatus, cfg *config.Config) bool {
	return live.Listening && live.Bind == cfg.WebBind() && live.Port == cfg.WebPort()
}

// webEnableFailure is a failed restart after the config was saved.
type webEnableFailure struct {
	webEnableResult
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func webStateOf(cfg *config.Config) webEnableState {
	return webEnableState{Enabled: cfg.Web.Enabled, Bind: cfg.WebBind(), Port: cfg.WebPort()}
}

func newWebEnableCmd() *cobra.Command {
	var (
		opts   webEnableOptions
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Turn the web listener on and restart the daemon to apply it",
		Long: `Set web.enabled (and, with --bind, web.bind), validate the result, write
leo.yaml, and gracefully restart a running daemon so the listener comes up. A
config that already says so is left alone and nothing restarts. Writing the
file drops its comments, as other leo commands that edit it do.

A non-loopback --bind needs web.allowed_hosts; pass --allow-host (repeatable)
for the host names or IPs clients will use.

With --json stdout is one object ({ok, changed, restarted, web:{enabled, bind,
port}}); progress goes to stderr and a failure is {error, message} with a
non-zero exit.`,
		Example: `  leo web enable
  leo web enable --bind 192.168.1.20 --allow-host 192.168.1.20 --allow-host leo.local --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			check := func() *jsonError {
				if err := opts.gracefulOptions.validate(); err != nil {
					return failJSON("invalid_args", err)
				}
				if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
					return failJSON("not_permitted", err)
				}
				return nil
			}
			if asJSON {
				if !opts.gracefulOptions.noGraceful {
					opts.gracefulOptions.graceful = true
				}
				return runJSON(cmd.OutOrStdout(), func() (any, error) {
					if je := check(); je != nil {
						return nil, je
					}
					return runWebEnableCmd(cmd, opts, cmd.ErrOrStderr(), true)
				})
			}
			if je := check(); je != nil {
				return errors.New(je.Message)
			}
			res, err := runWebEnableCmd(cmd, opts, cmd.OutOrStdout(), false)
			if err != nil {
				return err
			}
			printWebEnableText(cmd.OutOrStdout(), res)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.bind, "bind", "", "web.bind address to set (default: leave as is)")
	f.StringSliceVar(&opts.allowHosts, "allow-host", nil, "host or IP to add to web.allowed_hosts (repeatable)")
	f.BoolVar(&asJSON, "json", false, "print one JSON object on stdout; progress goes to stderr")
	addGracefulFlags(cmd, &opts.gracefulOptions)
	return markJSON(cmd)
}

func runWebEnableCmd(cmd *cobra.Command, opts webEnableOptions, progress io.Writer, asJSON bool) (webEnableResult, error) {
	cfg, err := loadConfig()
	if err != nil {
		if asJSON {
			return webEnableResult{}, failJSON("config_error", err)
		}
		return webEnableResult{}, err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	deps := webEnableDeps{
		daemonWeb: func(home string) (daemon.WebStatus, bool) { return daemon.DaemonWeb(ctx, home) },
		gracefulDeps: func(cfg *config.Config) gracefulDeps {
			d := newGracefulDepsTo(cfg, progress)
			if asJSON {
				d.isInteractive = func() bool { return false }
			}
			return d
		},
		save: saveConfig,
	}
	return runWebEnable(ctx, cfg, opts, deps)
}

func printWebEnableText(out io.Writer, res webEnableResult) {
	switch {
	case res.Restarted && !res.Changed:
		fmt.Fprintf(out, "The config already enabled the web listener, but the daemon was not serving it; daemon restarted (%s:%d).\n", res.Web.Bind, res.Web.Port)
	case !res.Changed:
		fmt.Fprintf(out, "The web listener is already enabled on %s:%d.\n", res.Web.Bind, res.Web.Port)
	case res.Restarted:
		fmt.Fprintf(out, "Web listener enabled on %s:%d; daemon restarted.\n", res.Web.Bind, res.Web.Port)
	default:
		fmt.Fprintf(out, "Web listener enabled on %s:%d. It takes effect when the daemon next starts.\n", res.Web.Bind, res.Web.Port)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
}
