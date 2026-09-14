package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/web"
	"github.com/spf13/cobra"
)

func newDispatchCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dispatch", Short: "Run and inspect headless subagents"}
	cmd.AddCommand(newDispatchRunCmd(), newConsultListCmd(), newConsultWatchCmd(), newDispatchShowCmd(), newDispatchCancelCmd())
	return cmd
}

func dispatchHTTP(ctx context.Context, cfg *config.Config, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", cfg.WebPort(), path), reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token, err := os.ReadFile(web.APITokenPath(cfg.StatePath()))
	if err != nil {
		return fmt.Errorf("reading API token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return fmt.Errorf("daemon: %s", envelope.Error)
	}
	return json.Unmarshal(envelope.Data, out)
}

func newDispatchRunCmd() *cobra.Command {
	var model, cwd, name, host string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "run <template> <prompt>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout < 0 {
			return fmt.Errorf("timeout must be non-negative")
		}
		cfg, res, err := dispatch(host)
		if err != nil {
			return err
		}
		if !res.Localhost {
			return fmt.Errorf("dispatch run is not supported for remote hosts")
		}
		if cwd == "" {
			cwd, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return err
		}
		var started consult.Started
		body := map[string]any{"template": args[0], "prompt": args[1], "model": model, "cwd": cwd, "name": name}
		if timeout > 0 {
			body["timeout_seconds"] = timeout.Seconds()
		}
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch", body, &started); err != nil {
			return err
		}
		var entries []consult.Entry
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodGet, "/api/dispatch/wait?id="+started.ID+"&timeout="+fmt.Sprintf("%g", timeout.Seconds()), nil, &entries); err != nil {
			return err
		}
		if len(entries) != 1 {
			return fmt.Errorf("daemon returned no dispatch result")
		}
		fmt.Fprintln(consultStdout, entries[0].Text)
		if entries[0].Status != consult.StatusDone {
			return fmt.Errorf("dispatch %s: %s", entries[0].Status, entries[0].Err)
		}
		return nil
	}}
	cmd.Flags().StringVarP(&model, "model", "m", "", "model override")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory")
	cmd.Flags().StringVar(&name, "name", "", "run name")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "optional run cap (unlimited when omitted)")
	addHostFlag(cmd, &host)
	return cmd
}

func newDispatchShowCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{Use: "show <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, res, err := dispatch(host)
		if err != nil {
			return err
		}
		if !res.Localhost {
			return fmt.Errorf("dispatch show is not supported for remote hosts")
		}
		var record consult.Record
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodGet, "/api/dispatch/"+args[0], nil, &record); err != nil {
			return err
		}
		return json.NewEncoder(consultStdout).Encode(record)
	}}
	addHostFlag(cmd, &host)
	return cmd
}

func newDispatchCancelCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{Use: "cancel <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, res, err := dispatch(host)
		if err != nil {
			return err
		}
		if !res.Localhost {
			return fmt.Errorf("dispatch cancel is not supported for remote hosts")
		}
		var record consult.Record
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch/"+args[0]+"/cancel", nil, &record); err != nil {
			return err
		}
		fmt.Fprintln(consultStdout, record.Status)
		return nil
	}}
	addHostFlag(cmd, &host)
	return cmd
}
