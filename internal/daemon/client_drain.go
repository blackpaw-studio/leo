package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/drain"
)

// ErrDrainUnsupported means the running daemon predates graceful restart
// (it has no /drain routes) or runs without a drain manager.
var ErrDrainUnsupported = errors.New("the running daemon does not support graceful restart")

// DrainBusy asks the daemon which agents are busy now.
func DrainBusy(ctx context.Context, workDir string) ([]drain.Probe, error) {
	var busy []drain.Probe
	err := drainCall(ctx, workDir, http.MethodGet, "/drain/busy", nil, &busy)
	return busy, err
}

// DrainBegin starts a drain whose pause notice names timeout.
func DrainBegin(ctx context.Context, workDir string, timeout time.Duration) (drain.Status, error) {
	var st drain.Status
	seconds := int((timeout + time.Second - 1) / time.Second)
	err := drainCall(ctx, workDir, http.MethodPost, "/drain", DrainBeginRequest{TimeoutSeconds: seconds}, &st)
	return st, err
}

// DrainStatus polls the drain; each call renews its lease.
func DrainStatus(ctx context.Context, workDir string) (drain.Status, error) {
	var st drain.Status
	err := drainCall(ctx, workDir, http.MethodGet, "/drain", nil, &st)
	return st, err
}

// DrainCommit persists the drain set for the next daemon. Without force it
// fails with drain.ErrNotDrained while agents are still working.
func DrainCommit(ctx context.Context, workDir string, force bool) error {
	return drainCall(ctx, workDir, http.MethodPost, "/drain/commit", DrainCommitRequest{Force: force}, nil)
}

// DrainCancel ends the drain and tells paused agents to resume.
func DrainCancel(ctx context.Context, workDir string) error {
	return drainCall(ctx, workDir, http.MethodPost, "/drain/cancel", DrainCancelRequest{}, nil)
}

// drainCall is Send with the status checks an old daemon needs: its mux
// answers unknown routes with a plain-text 404 or 405, not an envelope.
func drainCall(ctx context.Context, workDir, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		payload = data
	}
	status, resp, err := sendRaw(ctx, newUnixClient(SockPath(workDir)), method, "http://daemon"+path, payload)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		return ErrDrainUnsupported
	}
	var env Response
	if err := json.Unmarshal(resp, &env); err != nil {
		return fmt.Errorf("decoding %s %s response (status %d): %w", method, path, status, err)
	}
	if !env.OK {
		return drainResponseError(&env)
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decoding %s %s data: %w", method, path, err)
	}
	return nil
}

func drainResponseError(env *Response) error {
	if env.Code == ErrorCodeDrainUnavailable {
		return ErrDrainUnsupported
	}
	for _, c := range drainErrorCodes {
		if env.Code == c.code {
			return c.err
		}
	}
	return errors.New(env.Error)
}

// maxDrainResponseBytes bounds a drain response body.
const maxDrainResponseBytes = 1 << 20

func sendRaw(ctx context.Context, client *http.Client, method, url string, payload []byte) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, fmt.Errorf("creating request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("connecting to daemon: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDrainResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("reading response: %w", err)
	}
	return resp.StatusCode, data, nil
}
