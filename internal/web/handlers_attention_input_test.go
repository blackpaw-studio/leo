package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/observe"
)

const secretCommand = "rm -rf /srv/secret-project"

// attentionInputServer is a token-split server with a paired device, an
// agent blocked on a permission prompt whose reason carries the full
// command, and a real bus for /api/v1/events.
func attentionInputServer(t *testing.T) (s *Server, deviceToken string, raiseAgain func() string) {
	t.Helper()
	s = newTokenSplitServer(t)
	s.agentSvc.(*mockAgentService).records = []agent.Record{{Name: "a", Status: "running"}}
	bus := observe.NewBus()
	s.events = bus
	s.attention = observe.NewAttentionStore(bus)
	s.attention.RegisterToken("tok", "a")
	raise := func(detail string) {
		s.attention.SetByTokenNeedsInput("tok", observe.AttentionReason{
			Kind: observe.AttentionReasonPermission, Tool: "Bash", Detail: detail, Input: secretCommand,
		})
	}
	raise("rm")
	store := devices.NewStore(filepath.Join(t.TempDir(), "state"), nil)
	s.devices = store
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	_, deviceToken, err = store.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	// Each call is a new reason (a repeat of the same one publishes nothing)
	// and returns its detail.
	calls := 0
	return s, deviceToken, func() string {
		calls++
		detail := "rm again " + strings.Repeat("x", calls)
		raise(detail)
		return detail
	}
}

func TestStateHidesPermissionInputFromTheAgentToken(t *testing.T) {
	s, deviceToken, _ := attentionInputServer(t)
	for _, tc := range []struct {
		name, token string
		wantInput   bool
	}{{"operator", testAPIToken, true}, {"paired device", deviceToken, true}, {"agent token", testAgentToken, false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := requestAs(t, s, http.MethodGet, "/api/v1/state", tc.token, "")
			if w.Code != http.StatusOK {
				t.Fatalf("state = %d %s", w.Code, w.Body)
			}
			body := w.Body.String()
			if got := strings.Contains(body, secretCommand); got != tc.wantInput {
				t.Fatalf("command present = %v, want %v (%s)", got, tc.wantInput, body)
			}
			if got := strings.Contains(body, `"detail":"rm"`); got != tc.wantInput {
				t.Fatalf("detail present = %v, want %v: it is reason content, hidden with the input (%s)", got, tc.wantInput, body)
			}
			if !strings.Contains(body, `"tool":"Bash"`) || !strings.Contains(body, `"state":"needs_input"`) {
				t.Fatalf("kind/tool/state must stay for every caller (%s)", body)
			}
		})
	}
}

// readActivityFrame returns the next agent_activity frame's data.
func readActivityFrame(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	for i := 0; i < 20; i++ {
		event, data := readSSEFrame(t, br)
		if event == "agent_activity" {
			return data
		}
	}
	t.Fatal("no agent_activity frame")
	return ""
}

func TestEventsHidePermissionInputFromTheAgentTokenAndKeepTheRest(t *testing.T) {
	s, deviceToken, raiseAgain := attentionInputServer(t)
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	for _, tc := range []struct {
		name, token string
		wantInput   bool
	}{{"operator", testAPIToken, true}, {"paired device", deviceToken, true}, {"agent token", testAgentToken, false}} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = testHost
			req.Header.Set("Authorization", "Bearer "+tc.token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			guard := time.AfterFunc(5*time.Second, func() { _ = resp.Body.Close() })
			defer guard.Stop()
			br := bufio.NewReader(resp.Body)
			if event, _ := readSSEFrame(t, br); event != "hello" {
				t.Fatalf("first frame = %q, want hello", event)
			}

			detail := raiseAgain()
			data := readActivityFrame(t, br)

			if got := strings.Contains(data, secretCommand); got != tc.wantInput {
				t.Fatalf("command present = %v, want %v (%s)", got, tc.wantInput, data)
			}
			if got := strings.Contains(data, `"detail":"`+detail+`"`); got != tc.wantInput {
				t.Fatalf("detail present = %v, want %v (%s)", got, tc.wantInput, data)
			}
			if !strings.Contains(data, `"tool":"Bash"`) || !strings.Contains(data, `"state":"needs_input"`) {
				t.Fatalf("frame lost its other fields: %s", data)
			}
		})
	}
	// Redacting for one subscriber must not alter what another is sent.
	if got, _ := s.attention.Get("a"); got.Reason == nil || got.Reason.Input != secretCommand {
		t.Fatalf("stored attention = %+v, want the input intact", got)
	}
}
