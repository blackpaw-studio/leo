package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// taskFailureReportTimeout bounds one ReportTaskFailure round-trip, so an
// unresponsive daemon cannot hold a failed `leo run` open.
const taskFailureReportTimeout = 10 * time.Second

// TaskFailureRequest is the body of POST /chat/task-failure: a failed
// oneshot task run for the daemon to post as a card in its owner's thread.
type TaskFailureRequest struct {
	Task     string `json:"task"`
	RunID    string `json:"run_id"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error"`
	LogPath  string `json:"log_path"`
}

// ReportTaskFailure reports a failed run over the workspace's Unix socket.
// Callers treat an error as non-fatal: the run's own result is unaffected.
func ReportTaskFailure(ctx context.Context, workDir string, req TaskFailureRequest) error {
	ctx, cancel := context.WithTimeout(ctx, taskFailureReportTimeout)
	defer cancel()
	return reportTaskFailure(ctx, newUnixClient(SockPath(workDir)), "http://daemon", req)
}

// ReportTaskFailureHTTP is the test-friendly variant that talks to baseURL
// via a plain HTTP client.
func ReportTaskFailureHTTP(ctx context.Context, baseURL string, req TaskFailureRequest) error {
	return reportTaskFailure(ctx, &http.Client{Timeout: taskFailureReportTimeout}, baseURL, req)
}

func reportTaskFailure(ctx context.Context, cli *http.Client, baseURL string, req TaskFailureRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling task failure: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+chatTaskFailurePath, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("creating task failure request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(hreq)
	if err != nil {
		return fmt.Errorf("posting task failure: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out Response
	if json.Unmarshal(body, &out) == nil && out.Error != "" {
		return fmt.Errorf("task failure card: %s", out.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("task failure card: status %d", resp.StatusCode)
	}
	return nil
}
