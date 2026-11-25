package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/observe"
)

const (
	secretAskText   = "approve the deploy to prod-secret-cluster?"
	secretPermInput = "kubectl delete ns payroll-secret"
)

// publishOtherAgentChat puts another agent's chat traffic on the bus: an ask
// and a permission prompt (chat.message), its update (chat.updated), and the
// agent's seen stamp.
func publishOtherAgentChat(bus *observe.Bus) {
	msg := chat.Message{
		ID: "m1", Agent: "other", Direction: "outbound", Kind: "permission", Text: secretAskText, State: "open",
		Permission: &chat.Permission{Tool: "Bash", Summary: secretPermInput},
	}
	bus.Publish(observe.Event{Type: chat.EventMessage, Payload: &chat.MessagePayload{Agent: "other", Message: msg}})
	bus.Publish(observe.Event{Type: chat.EventUpdated, Payload: &chat.UpdatedPayload{Agent: "other", Message: msg, Change: chat.ChangeState}})
	bus.Publish(observe.Event{Type: chat.EventAgentSeen, Payload: &chat.AgentSeenPayload{Agent: "other", SeenAt: time.Now()}})
}

// TestEventsKeepOtherAgentsChatOffNonOperatorStreams: the REST chat and seen
// routes refuse the agent token, so its event stream must carry none of that
// data either. Operator and paired-device streams still get it.
func TestEventsKeepOtherAgentsChatOffNonOperatorStreams(t *testing.T) {
	s, deviceToken, raiseAgain := attentionInputServer(t)
	bus := s.events.(*observe.Bus)
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	for _, tc := range []struct {
		name, token string
		wantChat    bool
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

			publishOtherAgentChat(bus)
			raiseAgain() // the sentinel: every frame before it has been written

			var stream strings.Builder
			seen := map[string]bool{}
			for {
				event, data := readSSEFrame(t, br)
				stream.WriteString(event + " " + data + "\n")
				seen[event] = true
				if event == "agent_activity" {
					break
				}
			}
			for _, secret := range []string{secretAskText, secretPermInput} {
				if got := strings.Contains(stream.String(), secret); got != tc.wantChat {
					t.Errorf("%q present = %v, want %v\n%s", secret, got, tc.wantChat, stream.String())
				}
			}
			for _, ev := range []observe.EventType{chat.EventMessage, chat.EventUpdated, chat.EventAgentSeen} {
				if seen[string(ev)] != tc.wantChat {
					t.Errorf("%s frame present = %v, want %v", ev, seen[string(ev)], tc.wantChat)
				}
			}
		})
	}
}
