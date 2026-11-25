package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// Text another agent's conversation produced, on the carriers that are not
// an ask: the finished turn's final message and a failed turn's error (both
// attention.preview), agent_turn_completed's preview, what the agent is doing
// now (current_action.detail), and its self-reported task text.
const (
	secretFinal   = "final-message-secret: the acquisition closes friday"
	secretFailure = "turn-error-secret: auth to vault.internal failed"
	secretTurn    = "turn-preview-secret: wire the bonus to account 42"
	secretAction  = "action-secret: grep -r payroll-secret"
	secretTitle   = "title-secret: layoff plan"
	secretStatus  = "status-secret: drafting severance"
)

var conversationSecrets = []string{secretFinal, secretFailure, secretTurn, secretAction, secretTitle, secretStatus}

type staticActivity map[string]observe.AgentActivity

func (a staticActivity) Activities() map[string]observe.AgentActivity { return a }

// TestAgentConversationTextStaysOffNonOperatorStateAndStream: nothing another
// agent said or is doing reaches an agent-token reader, on the event stream
// or /api/v1/state; the operator and a paired device still get all of it.
func TestAgentConversationTextStaysOffNonOperatorStateAndStream(t *testing.T) {
	s, deviceToken, raiseAgain := attentionInputServer(t)
	bus := s.events.(*observe.Bus)
	svc := s.agentSvc.(*mockAgentService)
	svc.records = append(svc.records, agent.Record{Name: "other", Status: "running"})
	s.activity = staticActivity{"other": {Activity: observe.ActivityWorking, CurrentAction: &observe.Action{Kind: observe.ActionKindTool, Detail: secretAction}}}
	s.progress = observe.NewProgressStore(nil, bus)
	s.attention.RegisterToken("other-tok", "other")
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)

	for _, tc := range []struct {
		name, token string
		wantText    bool
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

			// A fresh title each run: an unchanged task publishes nothing.
			title, status := secretTitle+" "+tc.name, secretStatus
			if _, err := s.progress.Upsert("other", observe.TaskUpdate{ID: "t-" + strings.ReplaceAll(tc.name, " ", "-"), Title: &title, Status: &status}); err != nil {
				t.Fatal(err)
			}
			s.attention.SetByTokenFinished("other-tok", secretFinal)
			s.attention.SetByTokenErrored("other-tok", secretFailure)
			bus.Publish(observe.Event{Type: observe.EventAgentTurnCompleted, Payload: &observe.AgentTurnCompletedPayload{Agent: "other", Outcome: observe.TurnCompleted, Preview: secretTurn}})
			bus.Publish(observe.Event{Type: observe.EventAgentActivity, Payload: &observe.AgentActivityPayload{Agent: "other", Activity: observe.ActivityWorking, CurrentAction: &observe.Action{Kind: observe.ActionKindTool, Detail: secretAction}}})
			raiseAgain()

			var stream strings.Builder
			for {
				event, data := readSSEFrame(t, br)
				stream.WriteString(event + " " + data + "\n")
				if event == "agent_activity" && strings.Contains(data, `"agent":"a"`) {
					break
				}
			}
			state := requestAs(t, s, http.MethodGet, "/api/v1/state", tc.token, "")
			if state.Code != http.StatusOK {
				t.Fatalf("state = %d %s", state.Code, state.Body)
			}
			for where, body := range map[string]string{"stream": stream.String(), "state": state.Body.String()} {
				for _, secret := range conversationSecrets {
					if where == "state" && (secret == secretFinal || secret == secretTurn) {
						continue // the state holds the latest preview only, and no turn frame
					}
					if got := strings.Contains(body, secret); got != tc.wantText {
						t.Errorf("%s carries %q = %v, want %v", where, secret, got, tc.wantText)
					}
				}
			}
			// Still told that the other agent needs attention.
			for _, want := range []string{`"state":"errored"`, `"agent":"other"`} {
				if !strings.Contains(stream.String(), want) {
					t.Errorf("stream lost %s", want)
				}
			}
		})
	}
}
