package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// contextHookTimeout bounds the daemon round trip of a context hook, which
// runs synchronously at every session start and prompt: a dead or slow
// daemon delays the agent by at most this long.
var contextHookTimeout = 3 * time.Second

// contextHookEvents are the hook events claude accepts additionalContext
// from.
var contextHookEvents = map[string]bool{"SessionStart": true, "UserPromptSubmit": true}

// newDispatchContextCmd is a claude SessionStart / UserPromptSubmit hook for
// a supervised agent (LEO_ATTENTION_TOKEN): it asks the daemon what the
// session should be told about delegation and prints it as the hook's
// additionalContext. The daemon owns the content and what was told already.
// Every failure, and a dispatch run, prints nothing and exits 0: the agent
// then starts without the context, never blocked.
func newDispatchContextCmd() *cobra.Command {
	return &cobra.Command{Use: "context", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		token, port, sock := os.Getenv("LEO_ATTENTION_TOKEN"), os.Getenv("LEO_WEB_PORT"), os.Getenv("LEO_SOCKET")
		if os.Getenv("LEO_DISPATCH_ID") != "" || token == "" || (port == "" && sock == "") {
			return nil
		}
		event, text, err := requestAgentContext(cmd.Context(), cmd.InOrStdin(), token, port, sock)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "leo: delegation context: %v\n", err)
			return nil
		}
		if text == "" {
			return nil
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetEscapeHTML(false)
		return encoder.Encode(contextHookOutput{HookSpecificOutput: contextHookSpecific{HookEventName: event, AdditionalContext: text}})
	}}
}

// contextHookOutput is claude's hook output carrying added context; the
// struct fixes the field order.
type contextHookOutput struct {
	HookSpecificOutput contextHookSpecific `json:"hookSpecificOutput"`
}

type contextHookSpecific struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// requestAgentContext relays the hook payload with the launch's attention
// token and returns the hook's event and the context the daemon wants added.
// An event that takes no context is not asked about.
//
// The request goes to the daemon's unix socket (sock) when the launch names
// one: there the kernel tells the daemon which process connected, which is
// what lets a /clear rebind the agent's session. Only when the socket cannot
// be reached does it go over TCP (port), where the daemon cannot tell who is
// calling and so serves only the session the agent is already bound to.
func requestAgentContext(ctx context.Context, stdin io.Reader, token, port, sock string) (event, text string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return "", "", fmt.Errorf("invalid LEO_WEB_PORT %q", port)
		}
	}
	payload, err := readHookPayload(stdin, time.Now().Add(attentionReportTimeout))
	if err != nil {
		return "", "", err
	}
	var hook struct {
		Event string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(payload, &hook); err != nil {
		return "", "", err
	}
	if !contextHookEvents[hook.Event] {
		return "", "", nil
	}
	body, err := json.Marshal(map[string]any{"token": token, "payload": json.RawMessage(payload)})
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, contextHookTimeout)
	defer cancel()

	var resp *http.Response
	if sock != "" {
		resp, err = postContext(ctx, &http.Client{Transport: unixTransport(sock)}, "http://daemon/api/agent/context", body, false)
		if err != nil && port == "" {
			return "", "", err
		}
	}
	if resp == nil {
		if port == "" {
			return "", "", fmt.Errorf("no daemon to ask")
		}
		resp, err = postContext(ctx, &http.Client{}, fmt.Sprintf("http://127.0.0.1:%s/api/agent/context", port), body, true)
		if err != nil {
			return "", "", err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("context request returned status %d", resp.StatusCode)
	}
	var reply struct {
		Context string `json:"context"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHookContextBytes)).Decode(&reply); err != nil {
		return "", "", fmt.Errorf("decoding context reply: %w", err)
	}
	return hook.Event, reply.Context, nil
}

// unixTransport dials every request to the unix socket at path.
func unixTransport(path string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
}

// postContext POSTs body to url; bearer adds the agent API token (the TCP API
// requires it, the 0600 socket does not).
func postContext(ctx context.Context, client *http.Client, url string, body []byte, bearer bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("LEO_API_TOKEN"); bearer && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

// maxHookContextBytes bounds the daemon's reply, as claude bounds the
// context a hook may add.
const maxHookContextBytes = 1 << 20
