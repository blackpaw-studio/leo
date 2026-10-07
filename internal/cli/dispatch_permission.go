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
	"time"

	"github.com/spf13/cobra"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

// permissionPollSlack is how much longer the hook waits on the daemon than
// the daemon waits for a decision, so the daemon's "no decision" answer,
// not a client timeout, ends a normal wait.
const permissionPollSlack = 30 * time.Second

// newDispatchPermissionCmd is a dispatched claude's PermissionRequest hook.
// It hands the request to the orchestrator through the daemon and prints the
// decision as claude's hook output. Every failure prints nothing and exits
// 0: claude then shows its ordinary prompt in the pane, so a broken route
// can only cost the fallback, never block or approve anything.
func newDispatchPermissionCmd() *cobra.Command {
	return &cobra.Command{Use: "permission", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		id, configPath := os.Getenv("LEO_DISPATCH_ID"), os.Getenv("LEO_CONFIG")
		if id == "" || configPath == "" {
			return nil
		}
		decision, err := requestPermission(cmd.Context(), cmd.InOrStdin(), id, configPath)
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
	ctx, cancel := context.WithTimeout(ctx, cfg.DispatchApprovalTimeout()+permissionPollSlack)
	defer cancel()
	path := fmt.Sprintf("http://127.0.0.1:%d/api/dispatch/%s/permission", cfg.WebPort(), url.PathEscape(id))
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
	var envelope struct {
		Data consult.PermissionDecision `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return consult.PermissionDecision{}, fmt.Errorf("decode decision: %w", err)
	}
	return envelope.Data, nil
}
