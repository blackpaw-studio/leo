package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// SockPath returns the daemon socket path for a workspace.
func SockPath(workDir string) string {
	return filepath.Join(workDir, "state", "leo.sock")
}

// IsRunning checks if a daemon is listening on the workspace socket.
func IsRunning(workDir string) bool {
	return SocketHealthy(context.Background(), SockPath(workDir))
}

// SocketHealthy reports whether a leo daemon answers GET /health on the given
// unix socket path. Unlike IsRunning it takes the socket path directly, so it
// works against a forwarded remote daemon socket (e.g. the local end of a
// `leo host forward` StreamLocalForward). The probe is bounded to one second
// and honors ctx cancellation.
func SocketHealthy(ctx context.Context, sockPath string) bool {
	client := newUnixClient(sockPath)

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "http://daemon/health", nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Send sends a request to the daemon and returns the response. The caller's
// context is propagated to the HTTP round-trip so Ctrl-C cancels the call.
func Send(ctx context.Context, workDir, method, path string, body any) (*Response, error) {
	sockPath := SockPath(workDir)
	client := newUnixClient(sockPath)

	var httpReq *http.Request
	var err error

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}
		httpReq, err = http.NewRequestWithContext(ctx, method, "http://daemon"+path, bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
	} else {
		httpReq, err = http.NewRequestWithContext(ctx, method, "http://daemon"+path, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("connecting to daemon: %w", err)
	}
	defer resp.Body.Close()

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &result, nil
}

func newUnixClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// newUnixClientNoTimeout is like newUnixClient but without an overall request
// timeout. Used for long-poll endpoints (e.g. /task/await) where the caller's
// context.Context governs cancellation instead.
func newUnixClientNoTimeout(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// EnqueueRequest is the payload for EnqueueTask / EnqueueTaskHTTP.
//
// InvocationID is optional: when non-empty the daemon will track the
// invocation under that id (matching a marker the caller has already baked
// into Prompt). When empty the daemon auto-generates an id.
type EnqueueRequest struct {
	InvocationID string
	Session      string // logical session name; the router's FIFO key
	TmuxSession  string // concrete tmux session to inject into
	Task         string
	Prompt       string
	Channels     []string
	QueueMax     int
	Timeout      time.Duration
	// Ensure, when non-nil, tells the daemon to make sure the target agent is
	// injectable (spawn/resume as needed) before this invocation is injected.
	// Set by runPersistent for every persistent task, which always resolves a
	// target agent via config.ResolveTaskTarget. Nil is only used by callers
	// (e.g. tests) that inject directly without an agent target.
	Ensure *EnsureSpec
}

// EnqueueResponse is the daemon's reply to /task/enqueue.
type EnqueueResponse struct {
	Accepted     bool   `json:"accepted"`
	InvocationID string `json:"invocation_id"`
	Reason       string `json:"reason"`
}

// AwaitResponse is the daemon's reply to /task/await.
type AwaitResponse struct {
	OK           bool   `json:"ok"`
	SessionID    string `json:"session_id"`
	FinalMessage string `json:"final_message"`
	Err          string `json:"error"`
}

// EnqueueTask is the production wrapper: builds the Unix-socket URL from
// workDir and dispatches through the workspace daemon.
func EnqueueTask(ctx context.Context, workDir string, req EnqueueRequest) (EnqueueResponse, error) {
	cli := newUnixClient(SockPath(workDir))
	return enqueueTask(ctx, cli, "http://daemon", req)
}

// EnqueueTaskHTTP posts to /task/enqueue at the given baseURL using a plain
// HTTP client. Intended for tests; production callers should prefer EnqueueTask.
func EnqueueTaskHTTP(ctx context.Context, baseURL string, req EnqueueRequest) (EnqueueResponse, error) {
	return enqueueTask(ctx, &http.Client{Timeout: 30 * time.Second}, baseURL, req)
}

func enqueueTask(ctx context.Context, cli *http.Client, baseURL string, req EnqueueRequest) (EnqueueResponse, error) {
	body := map[string]any{
		"invocation_id":   req.InvocationID,
		"session":         req.Session,
		"tmux_session":    req.TmuxSession,
		"task":            req.Task,
		"prompt":          req.Prompt,
		"channels":        req.Channels,
		"queue_max":       req.QueueMax,
		"timeout_seconds": int(req.Timeout.Seconds()),
		"ensure":          req.Ensure,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return EnqueueResponse{}, fmt.Errorf("marshaling enqueue request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/task/enqueue", bytes.NewReader(raw))
	if err != nil {
		return EnqueueResponse{}, fmt.Errorf("creating enqueue request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(hreq)
	if err != nil {
		return EnqueueResponse{}, fmt.Errorf("posting enqueue: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EnqueueResponse{}, fmt.Errorf("enqueue: status %d", resp.StatusCode)
	}
	var out EnqueueResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return EnqueueResponse{}, fmt.Errorf("decoding enqueue response: %w", err)
	}
	return out, nil
}

// AwaitTask is the production wrapper: long-polls /task/await for the given
// invocation ID via the workspace daemon. The caller's context governs
// cancellation; the underlying HTTP client has no overall timeout.
func AwaitTask(ctx context.Context, workDir, invocationID string) (AwaitResponse, error) {
	cli := newUnixClientNoTimeout(SockPath(workDir))
	return awaitTask(ctx, cli, "http://daemon", invocationID)
}

// AwaitTaskHTTP is the test-friendly variant that talks to baseURL via a plain
// HTTP client.
func AwaitTaskHTTP(ctx context.Context, baseURL, invocationID string) (AwaitResponse, error) {
	return awaitTask(ctx, &http.Client{}, baseURL, invocationID)
}

func awaitTask(ctx context.Context, cli *http.Client, baseURL, invocationID string) (AwaitResponse, error) {
	u := baseURL + "/task/await?invocation_id=" + url.QueryEscape(invocationID)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return AwaitResponse{}, fmt.Errorf("creating await request: %w", err)
	}
	resp, err := cli.Do(hreq)
	if err != nil {
		return AwaitResponse{}, fmt.Errorf("awaiting task: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AwaitResponse{}, fmt.Errorf("await: status %d", resp.StatusCode)
	}
	var out AwaitResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return AwaitResponse{}, fmt.Errorf("decoding await response: %w", err)
	}
	return out, nil
}

// ReportTask is the production wrapper: posts a Stop-hook turn report to the
// workspace daemon. sessionName is accepted for backwards compatibility with
// existing callers but no longer sent — routing is invocation-id-driven.
func ReportTask(ctx context.Context, workDir, invocationID, sessionID, finalMessage, sessionName string) error {
	_ = sessionName
	cli := newUnixClient(SockPath(workDir))
	return reportTask(ctx, cli, "http://daemon", invocationID, sessionID, finalMessage)
}

// ReportTaskHTTP is the test-friendly variant that talks to baseURL via a plain
// HTTP client.
func ReportTaskHTTP(ctx context.Context, baseURL, invocationID, sessionID, finalMessage, sessionName string) error {
	_ = sessionName
	return reportTask(ctx, &http.Client{Timeout: 30 * time.Second}, baseURL, invocationID, sessionID, finalMessage)
}

// observeTaskRunPublishTimeout bounds a single PublishTaskRun round-trip.
// Task-run observability must never delay or fail a run because the daemon
// is slow or unreachable, so this is intentionally short relative to task
// timeouts (which run in minutes).
const observeTaskRunPublishTimeout = 2 * time.Second

// PublishTaskRun reports a task-run event to the daemon over the workspace's
// Unix socket, so it lands in the daemon's observe.RunLog — the only place
// internal/run's producers (a `leo run` subprocess, which never runs inside
// the daemon's own process) can announce a run from. The caller must treat
// any returned error as non-fatal: a manual `leo run` with no daemon running
// must still execute the task normally. See internal/run's Publisher wiring
// in internal/cli/run.go for the non-fatal wrapper.
func PublishTaskRun(ctx context.Context, workDir string, eventType observe.EventType, run observe.TaskRun) error {
	ctx, cancel := context.WithTimeout(ctx, observeTaskRunPublishTimeout)
	defer cancel()

	cli := newUnixClient(SockPath(workDir))
	return publishTaskRun(ctx, cli, "http://daemon", eventType, run)
}

// PublishTaskRunHTTP is the test-friendly variant that talks to baseURL via
// a plain HTTP client, with no timeout override beyond the caller's ctx.
func PublishTaskRunHTTP(ctx context.Context, baseURL string, eventType observe.EventType, run observe.TaskRun) error {
	return publishTaskRun(ctx, &http.Client{Timeout: observeTaskRunPublishTimeout}, baseURL, eventType, run)
}

func publishTaskRun(ctx context.Context, cli *http.Client, baseURL string, eventType observe.EventType, run observe.TaskRun) error {
	body := observeTaskRunReq{Type: string(eventType), Run: run}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling task-run event: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/observe/task-run", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("creating task-run publish request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(hreq)
	if err != nil {
		return fmt.Errorf("posting task-run event: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("publish task-run: status %d", resp.StatusCode)
	}
	return nil
}

func reportTask(ctx context.Context, cli *http.Client, baseURL, invocationID, sessionID, finalMessage string) error {
	body := map[string]any{
		"invocation_id": invocationID,
		"session_id":    sessionID,
		"final_message": finalMessage,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling report request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/task/report", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("creating report request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(hreq)
	if err != nil {
		return fmt.Errorf("posting report: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report: status %d", resp.StatusCode)
	}
	return nil
}
