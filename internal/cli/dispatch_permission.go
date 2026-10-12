package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

// permissionPollSlack is how much longer the hook waits on the daemon than
// the daemon waits for a decision, so the daemon's "no decision" answer,
// not a client timeout, ends a normal wait.
const permissionPollSlack = 30 * time.Second

// newDispatchPermissionCmd is a claude PermissionRequest hook. In a
// dispatched claude it hands the request to the orchestrator through the
// daemon; in a supervised agent (LEO_ATTENTION_TOKEN) it offers the prompt
// in Leo Chat. It prints the decision as claude's hook output. Every
// failure prints nothing and exits 0: claude then leaves its ordinary
// prompt to the pane, so a broken route can only cost the fallback, never
// block or approve anything.
func newDispatchPermissionCmd() *cobra.Command {
	return &cobra.Command{Use: "permission", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var decision consult.PermissionDecision
		var err error
		id, configPath := os.Getenv("LEO_DISPATCH_ID"), os.Getenv("LEO_CONFIG")
		token, port := os.Getenv("LEO_ATTENTION_TOKEN"), os.Getenv("LEO_WEB_PORT")
		switch {
		case id != "":
			if configPath == "" {
				return nil
			}
			decision, err = requestPermission(cmd.Context(), cmd.InOrStdin(), id, configPath)
		case token != "" && port != "":
			decision, err = requestAgentPermission(cmd.Context(), cmd.InOrStdin(), token, port)
		default:
			return nil
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "leo: permission request: %v\n", err)
			return nil
		}
		if decision.Behavior != "allow" && decision.Behavior != "deny" {
			return nil
		}
		out := map[string]any{"behavior": decision.Behavior}
		if decision.Behavior == "deny" && decision.Message != "" {
			out["message"] = decision.Message
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": out},
		})
	}}
}

func requestPermission(ctx context.Context, stdin io.Reader, id, configPath string) (consult.PermissionDecision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return consult.PermissionDecision{}, err
	}
	payload, err := readHookPayload(stdin, time.Now().Add(attentionReportTimeout))
	if err != nil {
		return consult.PermissionDecision{}, err
	}
	body, err := json.Marshal(map[string]any{"payload": json.RawMessage(payload)})
	if err != nil {
		return consult.PermissionDecision{}, err
	}
	path := fmt.Sprintf("http://127.0.0.1:%d/api/dispatch/%s/permission", cfg.WebPort(), url.PathEscape(id))
	return pollPermission(ctx, path, body, cfg.DispatchApprovalTimeout()+permissionPollSlack)
}

// requestAgentPermission offers a supervised agent's permission prompt in
// Leo Chat. The daemon applies chat.permission_timeout, which never exceeds
// config.MaxChatPermissionTimeout, so the client deadline needs no config.
func requestAgentPermission(ctx context.Context, stdin io.Reader, token, port string) (consult.PermissionDecision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := strconv.Atoi(port); err != nil {
		return consult.PermissionDecision{}, fmt.Errorf("invalid LEO_WEB_PORT %q", port)
	}
	payload, err := readHookPayload(stdin, time.Now().Add(attentionReportTimeout))
	if err != nil {
		return consult.PermissionDecision{}, err
	}
	body, err := json.Marshal(map[string]any{"token": token, "payload": json.RawMessage(payload)})
	if err != nil {
		return consult.PermissionDecision{}, err
	}
	path := fmt.Sprintf("http://127.0.0.1:%s/api/agent/permission", port)
	return pollPermission(ctx, path, body, config.MaxChatPermissionTimeout+permissionPollSlack)
}

// pollPermission long-polls path for a decision for at most wait.
func pollPermission(ctx context.Context, path string, body []byte, wait time.Duration) (consult.PermissionDecision, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return consult.PermissionDecision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("LEO_API_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return consult.PermissionDecision{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return consult.PermissionDecision{}, fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}
	return decodePermissionReply(io.LimitReader(resp.Body, 1<<20))
}

// decodePermissionReply accepts exactly one {ok: true, data} envelope and
// nothing after it. Anything else is an error, so the hook stays silent and
// claude falls back to its own prompt rather than act on a partial or
// ambiguous reply. An empty data (no decision) is valid.
func decodePermissionReply(body io.Reader) (consult.PermissionDecision, error) {
	var envelope struct {
		OK   bool                       `json:"ok"`
		Data consult.PermissionDecision `json:"data"`
	}
	dec := json.NewDecoder(body)
	if err := dec.Decode(&envelope); err != nil {
		return consult.PermissionDecision{}, fmt.Errorf("decode decision: %w", err)
	}
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		return consult.PermissionDecision{}, fmt.Errorf("decode decision: trailing data after the reply")
	}
	if !envelope.OK {
		return consult.PermissionDecision{}, fmt.Errorf("daemon reply is not ok")
	}
	switch envelope.Data.Behavior {
	case "", "allow", "deny":
		return envelope.Data, nil
	default:
		return consult.PermissionDecision{}, fmt.Errorf("unknown decision %q", envelope.Data.Behavior)
	}
}
