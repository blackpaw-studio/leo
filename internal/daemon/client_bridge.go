package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBodyBytes bounds how much of a failed response is read for its
// message.
const maxErrorBodyBytes = 4 << 10

func bridgePath(agent, leaf string) string {
	return "/api/bridge/" + url.PathEscape(agent) + "/" + leaf
}

// OpenBridgeStream opens agent's bridge command stream on the workspace
// daemon. The returned body yields one JSON command per line until the
// daemon ends the stream (EOF); cancel ctx or close the body to disconnect.
func OpenBridgeStream(ctx context.Context, workDir, agent string) (io.ReadCloser, error) {
	return openBridgeStream(ctx, newUnixClientNoTimeout(SockPath(workDir)), "http://daemon", agent)
}

func openBridgeStream(ctx context.Context, cli *http.Client, baseURL, agent string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+bridgePath(agent, "stream"), nil)
	if err != nil {
		return nil, fmt.Errorf("creating bridge stream request: %w", err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to daemon: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, httpStatusError("bridge stream", resp)
	}
	return resp.Body, nil
}

// PostBridgeReport posts one raw report body for agent to the workspace
// daemon, returning the daemon's message on any non-2xx status.
func PostBridgeReport(ctx context.Context, workDir, agent string, body []byte) error {
	return postBridgeReport(ctx, newUnixClient(SockPath(workDir)), "http://daemon", agent, body)
}

func postBridgeReport(ctx context.Context, cli *http.Client, baseURL, agent string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+bridgePath(agent, "report"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating bridge report request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return httpStatusError("bridge report", resp)
	}
	return nil
}

// httpStatusError describes a failed daemon response, preferring the
// envelope's error message over the raw body.
func httpStatusError(what string, resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	msg := strings.TrimSpace(string(data))
	var env Response
	if json.Unmarshal(data, &env) == nil && env.Error != "" {
		msg = env.Error
	}
	return fmt.Errorf("%s: daemon returned %d: %s", what, resp.StatusCode, msg)
}
