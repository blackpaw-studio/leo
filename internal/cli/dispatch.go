package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/blackpaw-studio/leo/internal/web"
	"github.com/spf13/cobra"
)

func dispatchCallerPane(environ []string) string {
	pane, _ := tmux.CallerPaneFromEnv(environ)
	return pane
}

func newDispatchCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dispatch", Short: "Run and inspect subagents"}
	cmd.AddCommand(newDispatchRunCmd(), newConsultListCmd(), newConsultWatchCmd(), newDispatchShowCmd(), newDispatchOutputCmd(), newDispatchCancelCmd(), newDispatchReleaseCmd(), newDispatchSendCmd(), newDispatchReportCmd(), newDispatchViewerCmd())
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
	var model, cwd, name, host, mode string
	var isolation string
	var notify bool
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
		if mode != "headless" && mode != "interactive" {
			return fmt.Errorf("mode must be headless or interactive")
		}
		body := map[string]any{"template": args[0], "prompt": args[1], "model": model, "cwd": cwd, "name": name, "mode": mode}
		if caller := os.Getenv("LEO_PROCESS_NAME"); caller != "" {
			body["from"] = caller
		}
		body["notify"] = notify
		if isolation != "" {
			body["isolation"] = isolation
		}
		if pane := dispatchCallerPane(os.Environ()); pane != "" {
			body["caller_pane_id"] = pane
		}
		if timeout > 0 {
			body["timeout_seconds"] = timeout.Seconds()
		}
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch", body, &started); err != nil {
			return err
		}
		var entries []consult.Entry
		waitID := started.ID
		if mode == "interactive" {
			waitID += "#1"
		}
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodGet, "/api/dispatch/wait?id="+url.QueryEscape(waitID)+"&timeout="+fmt.Sprintf("%g", timeout.Seconds()), nil, &entries); err != nil {
			return err
		}
		if len(entries) != 1 {
			return fmt.Errorf("daemon returned no dispatch result")
		}
		fmt.Fprintln(consultStdout, entries[0].Text)
		if mode == "interactive" && entries[0].Outcome == consult.TurnFinished {
			return nil
		}
		if entries[0].Status != consult.StatusDone {
			return fmt.Errorf("dispatch %s: %s", entries[0].Status, entries[0].Err)
		}
		return nil
	}}
	cmd.Flags().StringVarP(&model, "model", "m", "", "model override")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory")
	cmd.Flags().StringVar(&name, "name", "", "run name")
	cmd.Flags().StringVar(&mode, "mode", "headless", "execution mode (headless or interactive)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "optional run cap (unlimited when omitted)")
	cmd.Flags().BoolVar(&notify, "notify", true, "notify the caller when the dispatch completes")
	cmd.Flags().StringVar(&isolation, "isolation", "", "execution isolation (worktree)")
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
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodGet, "/api/dispatch/"+url.PathEscape(args[0]), nil, &record); err != nil {
			return err
		}
		return encodeDispatchShow(consultStdout, record, time.Now())
	}}
	addHostFlag(cmd, &host)
	return cmd
}

func encodeDispatchShow(out io.Writer, record consult.Record, now time.Time) error {
	return json.NewEncoder(out).Encode(struct {
		consult.Record
		ElapsedSeconds float64 `json:"elapsed_seconds"`
	}{Record: record, ElapsedSeconds: record.Elapsed(now).Seconds()})
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
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch/"+url.PathEscape(args[0])+"/cancel", nil, &record); err != nil {
			return err
		}
		fmt.Fprintln(consultStdout, record.Status)
		return nil
	}}
	addHostFlag(cmd, &host)
	return cmd
}

func newDispatchReleaseCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{Use: "release <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, res, err := dispatch(host)
		if err != nil {
			return err
		}
		if !res.Localhost {
			return fmt.Errorf("dispatch release is not supported for remote hosts")
		}
		var record consult.Record
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch/"+url.PathEscape(args[0])+"/release", nil, &record); err != nil {
			return err
		}
		fmt.Fprintln(consultStdout, record.Status)
		return nil
	}}
	addHostFlag(cmd, &host)
	return cmd
}

func newDispatchSendCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{Use: "send <id> <message>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, res, err := dispatch(host)
		if err != nil {
			return err
		}
		if !res.Localhost {
			return fmt.Errorf("dispatch send is not supported for remote hosts")
		}
		var result consult.SendResult
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodPost, "/api/dispatch/"+url.PathEscape(args[0])+"/send", map[string]string{"message": args[1]}, &result); err != nil {
			return err
		}
		fmt.Fprintf(consultStdout, "%s %t\n", result.TurnID, result.Delivered)
		return nil
	}}
	addHostFlag(cmd, &host)
	return cmd
}

// dispatch report is intentionally a no-op outside interactive harnesses:
// Codex/Claude invoke every configured hook for ordinary sessions too.
func newDispatchReportCmd() *cobra.Command {
	return &cobra.Command{Use: "report", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		id, configPath := os.Getenv("LEO_DISPATCH_ID"), os.Getenv("LEO_CONFIG")
		if id == "" || configPath == "" {
			return nil
		}
		deadline := time.Now().Add(20 * time.Second)
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		type readResult struct {
			data []byte
			err  error
		}
		read := make(chan readResult, 1)
		go func() { data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); read <- readResult{data, err} }()
		var payload []byte
		select {
		case result := <-read:
			payload, err = result.data, result.err
		case <-time.After(time.Until(deadline)):
			return context.DeadlineExceeded
		}
		if err != nil {
			return err
		}
		if !json.Valid(payload) {
			return fmt.Errorf("invalid hook payload")
		}
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		eventID := fmt.Sprintf("%x", buf)
		body, err := json.Marshal(map[string]any{"event_id": eventID, "payload": json.RawMessage(payload)})
		if err != nil {
			return err
		}
		token := os.Getenv("LEO_API_TOKEN")
		path := fmt.Sprintf("http://127.0.0.1:%d/api/dispatch/%s/report", cfg.WebPort(), url.PathEscape(id))
		var last error
		for attempt := 0; attempt < 3 && time.Now().Before(deadline); attempt++ {
			ctx, cancel := context.WithDeadline(context.Background(), minTime(deadline, time.Now().Add(3*time.Second)))
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				resp, callErr := (&http.Client{}).Do(req)
				if callErr == nil {
					resp.Body.Close()
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						cancel()
						return nil
					}
					last = fmt.Errorf("report returned status %d", resp.StatusCode)
				} else {
					last = callErr
				}
			} else {
				last = err
			}
			cancel()
			if attempt < 2 {
				select {
				case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
				case <-time.After(time.Until(deadline)):
				}
			}
		}
		if last == nil {
			last = context.DeadlineExceeded
		}
		return last
	}}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
