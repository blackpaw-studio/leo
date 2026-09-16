package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/spf13/cobra"
)

var viewerExecCommandContext = exec.CommandContext
var viewerLocateTmux = tmux.Locate
var viewerExecutable = os.Executable

type viewerOverrides struct {
	Placement *string `json:"placement,omitempty"`
	MaxPanes  *int    `json:"max_panes,omitempty"`
}

func tmuxFormatLiteral(s string) string { return strings.ReplaceAll(s, "#", "##") }

func newDispatchViewerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "viewer", Short: "Manage dispatch viewers in a tmux session"}
	cmd.AddCommand(newViewerMenuCmd(), newViewerSetCmd(), newViewerCloseFinishedCmd(), newViewerSaveDefaultCmd())
	return cmd
}

func viewerContext() (*config.Config, string, string, error) {
	cfg, res, err := dispatch("")
	if err != nil {
		return nil, "", "", err
	}
	if !res.Localhost {
		return nil, "", "", fmt.Errorf("dispatch viewer commands are local-only")
	}
	tmuxPath, err := viewerLocateTmux()
	if err != nil {
		return nil, "", "", err
	}
	path, err := configPath()
	return cfg, tmuxPath, path, err
}

func readViewerOverrides(ctx context.Context, tmuxPath, session string) (viewerOverrides, error) {
	out, err := viewerExecCommandContext(ctx, tmuxPath, tmux.Args("show-options", "-t", tmux.Target(session))...).Output()
	if err != nil {
		id, resolveErr := resolvedViewerSession(ctx, tmuxPath, session)
		if resolveErr != nil {
			return viewerOverrides{}, err
		}
		out, err = viewerExecCommandContext(ctx, tmuxPath, tmux.Args("show-options", "-t", id)...).Output()
		if err != nil {
			return viewerOverrides{}, err
		}
	}
	var result viewerOverrides
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "@leo_viewer_placement":
			if value != "pane" && value != "window" {
				return result, fmt.Errorf("placement must be pane or window")
			}
			v := value
			result.Placement = &v
		case "@leo_viewer_max_panes":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 6 {
				return result, fmt.Errorf("max_panes must be between 1 and 6")
			}
			result.MaxPanes = &n
		default:
			if strings.HasPrefix(key, "@leo_viewer_") {
				return result, fmt.Errorf("unknown viewer setting %q", strings.TrimPrefix(key, "@leo_viewer_"))
			}
		}
	}
	return result, nil
}

func displayViewerError(ctx context.Context, tmuxPath, session string, err error) error {
	_ = viewerExecCommandContext(ctx, tmuxPath, tmux.Args("display-message", "-t", tmux.PaneTarget(session), err.Error())...).Run()
	return err
}

func newViewerMenuCmd() *cobra.Command {
	var session string
	cmd := &cobra.Command{Use: "menu", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, tmuxPath, cfgPath, err := viewerContext()
		if err != nil {
			return err
		}
		overrides, err := readViewerOverrides(cmd.Context(), tmuxPath, session)
		if err != nil {
			return displayViewerError(cmd.Context(), tmuxPath, session, err)
		}
		placement, cap := cfg.DispatchViewerPlacement(), cfg.DispatchViewerMaxPanes()
		if overrides.Placement != nil {
			placement = *overrides.Placement
		}
		if overrides.MaxPanes != nil {
			cap = *overrides.MaxPanes
		}
		nextPlacement := "pane"
		from, to := "windows", "panes"
		if placement == "pane" {
			nextPlacement, from, to = "window", "panes", "windows"
		}
		nextCap := cap%6 + 1
		leo, err := viewerExecutable()
		if err != nil {
			return err
		}
		args := tmux.Args("display-menu", "-t", tmux.PaneTarget(session), "-T", " leo · "+tmuxFormatLiteral(session)+" ",
			fmt.Sprintf("Viewer placement: %s  → %s", from, to), "p", tmux.ViewerMenuAction(leo, cfgPath, session, "set placement="+nextPlacement),
			fmt.Sprintf("Max panes: %d → %d", cap, nextCap), "m", tmux.ViewerMenuAction(leo, cfgPath, session, fmt.Sprintf("set max_panes=%d", nextCap)), "",
			"Close finished viewers", "c", tmux.ViewerMenuAction(leo, cfgPath, session, "close-finished"), "Save as default", "s", tmux.ViewerMenuAction(leo, cfgPath, session, "save-default"))
		if err := viewerExecCommandContext(cmd.Context(), tmuxPath, args...).Run(); err != nil {
			return err
		}
		return nil
	}}
	cmd.Flags().StringVar(&session, "session", "", "tmux session")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func newViewerSetCmd() *cobra.Command {
	var session string
	cmd := &cobra.Command{Use: "set key=value", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, tmuxPath, _, err := viewerContext()
		if err != nil {
			return err
		}
		key, value, ok := strings.Cut(args[0], "=")
		if !ok {
			return displayViewerError(cmd.Context(), tmuxPath, session, fmt.Errorf("expected key=value"))
		}
		switch key {
		case "placement":
			if value != "pane" && value != "window" {
				err = fmt.Errorf("placement must be pane or window")
			}
		case "max_panes":
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || n > 6 {
				err = fmt.Errorf("max_panes must be between 1 and 6")
			}
		default:
			err = fmt.Errorf("unknown viewer setting %q", key)
		}
		if err != nil {
			return displayViewerError(cmd.Context(), tmuxPath, session, err)
		}
		optionArgs := func(target string) []string { return tmux.Args("set-option", "-t", target, "@leo_viewer_"+key, value) }
		if out, err := viewerExecCommandContext(cmd.Context(), tmuxPath, optionArgs(tmux.Target(session))...).CombinedOutput(); err != nil {
			// set-option parses its target as a window on tmux 3.6, where the
			// exact-session =name syntax is treated literally. Preserve the
			// established argv contract first, then retry against the resolved ID.
			id, resolveErr := resolvedViewerSession(cmd.Context(), tmuxPath, session)
			if resolveErr != nil {
				return displayViewerError(cmd.Context(), tmuxPath, session, fmt.Errorf("setting viewer option: %w: %s", err, strings.TrimSpace(string(out))))
			}
			if retryOut, retryErr := viewerExecCommandContext(cmd.Context(), tmuxPath, optionArgs(id)...).CombinedOutput(); retryErr != nil {
				return displayViewerError(cmd.Context(), tmuxPath, session, fmt.Errorf("setting viewer option: %w: %s", retryErr, strings.TrimSpace(string(retryOut))))
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&session, "session", "", "tmux session")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func resolvedViewerSession(ctx context.Context, tmuxPath, session string) (string, error) {
	out, err := viewerExecCommandContext(ctx, tmuxPath, tmux.Args("display-message", "-p", "-t", tmux.PaneTarget(session), "#{session_id}")...).Output()
	return strings.TrimSpace(string(out)), err
}

func newViewerCloseFinishedCmd() *cobra.Command {
	var session string
	cmd := &cobra.Command{Use: "close-finished", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, tmuxPath, _, err := viewerContext()
		if err != nil {
			return err
		}
		id, err := resolvedViewerSession(cmd.Context(), tmuxPath, session)
		if err != nil {
			return displayViewerError(cmd.Context(), tmuxPath, session, err)
		}
		var out any
		if err = dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch/viewer/close-finished", map[string]string{"session_id": id}, &out); err != nil {
			return displayViewerError(cmd.Context(), tmuxPath, session, err)
		}
		return nil
	}}
	cmd.Flags().StringVar(&session, "session", "", "tmux session")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func clearViewerOverrides(ctx context.Context, tmuxPath, session string) error {
	for _, key := range []string{"@leo_viewer_placement", "@leo_viewer_max_panes"} {
		args := func(target string) []string { return tmux.Args("set-option", "-u", "-t", target, key) }
		if err := viewerExecCommandContext(ctx, tmuxPath, args(tmux.Target(session))...).Run(); err != nil {
			id, resolveErr := resolvedViewerSession(ctx, tmuxPath, session)
			if resolveErr != nil {
				return err
			}
			if retryErr := viewerExecCommandContext(ctx, tmuxPath, args(id)...).Run(); retryErr != nil {
				return retryErr
			}
		}
	}
	return nil
}
func newViewerSaveDefaultCmd() *cobra.Command {
	var session string
	cmd := &cobra.Command{Use: "save-default", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, tmuxPath, _, err := viewerContext()
		if err != nil {
			return err
		}
		overrides, err := readViewerOverrides(cmd.Context(), tmuxPath, session)
		if err != nil {
			return displayViewerError(cmd.Context(), tmuxPath, session, err)
		}
		var result struct{ Saved, Reloaded bool }
		if err = dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch/viewer/save-default", overrides, &result); err != nil {
			return displayViewerError(cmd.Context(), tmuxPath, session, err)
		}
		if !result.Saved || !result.Reloaded {
			return displayViewerError(cmd.Context(), tmuxPath, session, fmt.Errorf("saved, reload failed"))
		}
		if err = clearViewerOverrides(cmd.Context(), tmuxPath, session); err != nil {
			return displayViewerError(cmd.Context(), tmuxPath, session, fmt.Errorf("saved defaults but clearing session overrides: %w", err))
		}
		return nil
	}}
	cmd.Flags().StringVar(&session, "session", "", "tmux session")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}
