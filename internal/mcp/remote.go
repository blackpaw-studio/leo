package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// parseTarget splits a leo_send_message `to` argument. A bare name is a local
// agent and returned untouched, so existing callers behave exactly as before.
// "agent@host" addresses an agent on a peer host (a key in client.hosts); any
// other use of '@' is rejected rather than guessed at.
func parseTarget(to string) (agent, host string, remote bool, err error) {
	if !strings.Contains(to, "@") {
		return to, "", false, nil
	}
	if strings.IndexFunc(to, isSpace) >= 0 {
		return "", "", false, fmt.Errorf("target %q must not contain whitespace", to)
	}
	if strings.Count(to, "@") != 1 {
		return "", "", false, fmt.Errorf("target %q must have exactly one '@' (agent@host)", to)
	}
	agent, host, _ = strings.Cut(to, "@")
	switch {
	case agent == "":
		return "", "", false, errors.New("target is missing the agent name before '@' (agent@host)")
	case host == "":
		return "", "", false, errors.New("target is missing the host name after '@' (agent@host)")
	}
	return agent, host, true, nil
}

func isSpace(r rune) bool { return strings.ContainsRune(" \t\r\n\v\f", r) }

// remoteTimeout bounds one request to a peer host. There is no retry: a peer
// that is asleep or unreachable fails the send, and the agent decides.
const remoteTimeout = 10 * time.Second

// maxRemoteResponseBytes caps what is read back from a peer. A peer is
// outside this machine's trust boundary, unlike the local daemon.
const maxRemoteResponseBytes = 1 << 20

// maxRemoteErrorChars bounds how much of a peer's error text reaches the
// agent's tool result.
const maxRemoteErrorChars = 200

// remoteHost is a peer as the daemon's GET /api/remote-host/{host} describes
// it: where to reach it, the bearer token its api_clients entry issued, and
// the name this machine is registered under there.
type remoteHost struct {
	Name       string `json:"-"`
	URL        string `json:"api_url"`
	Token      string `json:"api_token"`
	ClientName string `json:"api_client"`
}

// remoteHost asks the local daemon to resolve client.hosts.<name>. The MCP
// process never reads leo.yaml itself: the daemon holds the config and
// resolves the token's secret reference.
func (c *daemonClient) remoteHost(name string) (remoteHost, error) {
	data, err := c.do(http.MethodGet, "/api/remote-host/"+url.PathEscape(name), nil)
	if err != nil {
		return remoteHost{}, err
	}
	var h remoteHost
	if err := json.Unmarshal(data, &h); err != nil {
		return remoteHost{}, fmt.Errorf("decode remote host %q: %w", name, err)
	}
	if h.URL == "" || h.Token == "" || h.ClientName == "" {
		return remoteHost{}, fmt.Errorf("daemon returned an incomplete entry for host %q", name)
	}
	h.Name = name
	return h, nil
}

// remoteClient talks to peer daemons' api_clients routes. Its http.Client
// never follows redirects: the bearer header must reach only the configured
// api_url, so a redirecting (or misconfigured) endpoint is an error, not a
// detour that carries the token elsewhere.
type remoteClient struct {
	http *http.Client
}

func newRemoteClient() *remoteClient {
	return &remoteClient{http: &http.Client{
		Timeout:       remoteTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// withRemote swaps the peer transport; tests use it to capture the wire.
func withRemote(rc *remoteClient) registryOption {
	return func(r *registry) { r.remote = rc }
}

// send delivers text to agent on h, as `from`. The text carries no
// "[message from …]" prefix: the receiving daemon stamps one from the
// authenticated identity.
func (rc *remoteClient) send(ctx context.Context, h remoteHost, agent, from, text string) error {
	body, err := json.Marshal(map[string]string{"text": text, "from": from})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	_, err = rc.do(ctx, h, agent, http.MethodPost, "/web/agent/"+url.PathEscape(agent)+"/message", body)
	return err
}

// listAgents returns the peer's message-able agents (name and status).
func (rc *remoteClient) listAgents(ctx context.Context, h remoteHost) (json.RawMessage, error) {
	return rc.do(ctx, h, "", http.MethodGet, "/web/client/agents", nil)
}

// do issues one authenticated request to h and returns the response's data on
// success. Every failure is a mapRemoteError: no error text here can carry the
// token.
func (rc *remoteClient) do(ctx context.Context, h remoteHost, agent, method, path string, body []byte) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(h.URL, "/")+path, reader)
	if err != nil {
		return nil, fmt.Errorf("host %s has an unusable api_url", h.Name)
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := rc.http.Do(req)
	if err != nil {
		return nil, mapRemoteError(h, agent, 0, nil, err, rc.http.Timeout)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteResponseBytes))
	if err != nil {
		return nil, mapRemoteError(h, agent, 0, nil, err, rc.http.Timeout)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, mapRemoteError(h, agent, resp.StatusCode, raw, nil, rc.http.Timeout)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, mapRemoteError(h, agent, resp.StatusCode, raw, errors.New("unexpected response"), rc.http.Timeout)
	}
	return env.Data, nil
}

// mapRemoteError turns a failed peer call into an error an agent can act on.
// status is 0 for a transport failure (err set). The peer's own error text is
// passed through only for 400/403/5xx, bounded and stripped of control
// characters, and with the token redacted in case a peer echoes it back.
func mapRemoteError(h remoteHost, agent string, status int, body []byte, err error, timeout time.Duration) error {
	if status == 0 {
		return transportError(h, err, timeout)
	}
	detail := peerErrorText(h, body)
	switch {
	case status == http.StatusUnauthorized:
		return fmt.Errorf("host %s rejected the api_token (check client.hosts.%s.api_token and the api_clients entry %q there)", h.Name, h.Name, h.ClientName)
	case status == http.StatusForbidden:
		return fmt.Errorf("not permitted to %s on %s: %s", forTarget("message", agent), h.Name, orDefault(detail, "forbidden by the host's api_clients can_message"))
	case status == http.StatusNotFound && agent != "":
		return fmt.Errorf("no agent %s on %s", agent, h.Name)
	case status == http.StatusNotFound:
		return fmt.Errorf("host %s does not serve this request (is it running a leo version that supports cross-host messaging?)", h.Name)
	case status == http.StatusBadRequest:
		return fmt.Errorf("host %s rejected the request: %s", h.Name, orDefault(detail, "bad request"))
	case status >= 300 && status < 400:
		return fmt.Errorf("host %s redirected the request (status %d); client.hosts.%s.api_url must point at the daemon directly", h.Name, status, h.Name)
	default:
		return fmt.Errorf("host %s returned status %d: %s", h.Name, status, orDefault(detail, "no detail"))
	}
}

func forTarget(verb, agent string) string {
	if agent == "" {
		return verb + " agents"
	}
	return verb + " " + agent
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// transportError classifies a failure to get any HTTP response.
func transportError(h remoteHost, err error, timeout time.Duration) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Errorf("host %s did not answer within %s", h.Name, timeout)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("request to host %s was cancelled", h.Name)
	}
	// Name the destination, never the underlying text: url.Error wraps the
	// request URL, and an OS error is not worth an agent's context.
	return fmt.Errorf("host %s unreachable at %s", h.Name, h.URL)
}

// peerErrorText extracts the peer's {"error": ...} message, made safe to show:
// the token redacted, control characters dropped, length bounded.
func peerErrorText(h remoteHost, body []byte) string {
	var env apiEnvelope
	if json.Unmarshal(body, &env) != nil || env.Error == "" {
		return ""
	}
	text := strings.ReplaceAll(env.Error, h.Token, "[redacted]")
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	if runes := []rune(text); len(runes) > maxRemoteErrorChars {
		text = string(runes[:maxRemoteErrorChars]) + "…"
	}
	return strings.TrimSpace(text)
}

// sendRemote delivers message to agent on the peer host. The permission check
// has already run against the full agent@host target.
func (r *registry) sendRemote(client *daemonClient, processName, agent, host, message string) (string, error) {
	// A dispatch child's process name (dispatch:<id>) is outside the charset
	// a peer accepts in `from`, and it names no agent a reply could reach.
	if r.dispatchID != "" || strings.HasPrefix(processName, "dispatch:") {
		return "", errors.New("a leo_dispatch subagent cannot message another host; report to your orchestrator, which can")
	}
	h, err := client.remoteHost(host)
	if err != nil {
		return "", err
	}
	from := h.ClientName
	if processName != "" {
		from += "#" + processName
	}
	if err := r.remote.send(context.Background(), h, agent, from, message); err != nil {
		return "", err
	}
	return fmt.Sprintf("Sent message to %s@%s", agent, host), nil
}

// listRemoteAgents lists the agents the peer's api_clients entry for this
// host may message.
func (r *registry) listRemoteAgents(client *daemonClient, host string) (string, error) {
	if r.dispatchID != "" {
		return "", errors.New("a leo_dispatch subagent cannot message another host, so it cannot list one's agents")
	}
	if strings.Contains(host, "@") {
		return "", fmt.Errorf("host %q must be a client.hosts name, without '@'", host)
	}
	h, err := client.remoteHost(host)
	if err != nil {
		return "", err
	}
	data, err := r.remote.listAgents(context.Background(), h)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
