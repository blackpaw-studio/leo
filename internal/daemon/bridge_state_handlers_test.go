package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func TestBridgeStreamWritesStateLines(t *testing.T) {
	workDir, hub, _ := startBridgeServer(t)
	r := openStream(t, workDir, hub, bridgeAgent)
	if err := hub.SetState(bridgeAgent, bridge.StateSnapshot{Delegation: bridge.DelegationState{Enabled: true, Section: "roles"}}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(readLine(t, r)), &got); err != nil {
		t.Fatal(err)
	}
	if got["op"] != "state" || got["delegation"].(map[string]any)["section"] != "roles" {
		t.Fatalf("state line = %v", got)
	}
}

func postRequest(t *testing.T, workDir string) *http.Response {
	t.Helper()
	body := `{"type":"request","op":"dispatch.cancel","dispatch_id":"d1"}`
	req, _ := http.NewRequestWithContext(bridgeTestCtx(t), http.MethodPost,
		"http://daemon/api/bridge/"+bridgeAgent+"/report?launch="+bridgeLaunch, strings.NewReader(body))
	resp, err := newUnixClient(SockPath(workDir)).Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestBridgeRequestStatuses(t *testing.T) {
	cases := []struct {
		name    string
		handler bridge.RequestHandler
		status  int
	}{
		{"served", func(string, string, string) error { return nil }, http.StatusOK},
		{"denied", func(string, string, string) error { return fmt.Errorf("%w: not yours", bridge.ErrRequestDenied) }, http.StatusForbidden},
		{"unserved", nil, http.StatusNotImplemented},
		{"failed", func(string, string, string) error { return fmt.Errorf("cancel broke") }, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir, hub, _ := startBridgeServer(t)
			hub.SetRequestHandler(tc.handler)
			if resp := postRequest(t, workDir); resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}
