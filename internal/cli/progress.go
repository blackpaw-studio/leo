package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// progressTimeout bounds one `leo progress` call, so a dead daemon never
// stalls the script reporting through it.
const progressTimeout = 5 * time.Second

// maxProgressReplyBytes bounds how much of a daemon reply is read.
const maxProgressReplyBytes = 64 << 10

// notInAgentExit is the status `leo progress` exits with outside an agent.
const notInAgentExit = 2

func newProgressCmd() *cobra.Command {
	var (
		title, status          string
		percent                float64
		eta                    time.Duration
		indeterminate          bool
		done, fail, cancelTask bool
	)
	cmd := &cobra.Command{
		Use:   "progress <id>",
		Short: "Report progress of a long-running task from inside an agent",
		Long: `Report progress of a long-running task (a build, a deploy, a test sweep) so
Leo can show it next to this agent. Run it from the agent's own session: it
authenticates with the agent's launch token, and only ever writes this
agent's tasks. Omitted flags keep their value; the first report for an id
needs --title.

Examples:
  leo progress build --title "Xcode build" --percent 10 --eta 5m
  leo progress build --percent 62 --status "Compiling 412/980"
  leo progress deploy --title "Deploy" --indeterminate
  leo progress build --done`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			update := map[string]any{"id": args[0]}
			if flags.Changed("title") {
				update["title"] = title
			}
			if flags.Changed("percent") {
				update["percent"] = percent
			}
			if indeterminate {
				update["indeterminate"] = true
			}
			if flags.Changed("status") {
				update["status"] = status
			}
			if flags.Changed("eta") {
				if eta < 0 {
					return fmt.Errorf("--eta must not be negative")
				}
				update["eta_seconds"] = eta.Seconds()
			}
			switch {
			case done:
				update["state"] = "succeeded"
			case fail:
				update["state"] = "failed"
			case cancelTask:
				update["state"] = "cancelled"
			}
			return reportProgress(cmd.Context(), update)
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "task title (required on the first report)")
	f.Float64Var(&percent, "percent", 0, "progress, 0-100")
	f.BoolVar(&indeterminate, "indeterminate", false, "show an indeterminate bar instead of a percentage")
	f.StringVar(&status, "status", "", "short status text, e.g. \"Compiling 412/980\"")
	f.DurationVar(&eta, "eta", 0, "estimated time remaining, e.g. 90s or 5m")
	f.BoolVar(&done, "done", false, "mark the task succeeded")
	f.BoolVar(&fail, "fail", false, "mark the task failed")
	f.BoolVar(&cancelTask, "cancel", false, "mark the task cancelled")
	cmd.MarkFlagsMutuallyExclusive("percent", "indeterminate")
	cmd.MarkFlagsMutuallyExclusive("done", "fail", "cancel")
	return cmd
}

// reportProgress sends one update with the agent's launch token, which is
// all the daemon uses to decide whose task it is.
func reportProgress(ctx context.Context, update map[string]any) error {
	if id := os.Getenv("LEO_DISPATCH_ID"); id != "" {
		return fmt.Errorf("leo progress is only for supervised Leo agents, not a leo_dispatch subagent (dispatch %s); report to your orchestrator instead", id)
	}
	token, port := os.Getenv("LEO_ATTENTION_TOKEN"), os.Getenv("LEO_WEB_PORT")
	if token == "" || port == "" {
		return exitCodeError{code: notInAgentExit, err: errors.New("not inside a Leo agent: LEO_ATTENTION_TOKEN and LEO_WEB_PORT are not set (run this from an agent's own session)")}
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("invalid LEO_WEB_PORT %q", port)
	}
	update["token"] = token
	body, err := json.Marshal(update)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, progressTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+port+"/api/agent/progress", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if api := os.Getenv("LEO_API_TOKEN"); api != "" {
		req.Header.Set("Authorization", "Bearer "+api)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reporting progress: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("progress rejected: %s", daemonErrorText(resp))
}

// daemonErrorText is the daemon's {"error": ...} message, else the status.
func daemonErrorText(resp *http.Response) string {
	var reply struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProgressReplyBytes))
	if json.Unmarshal(raw, &reply) == nil && reply.Error != "" {
		return reply.Error
	}
	return resp.Status
}
