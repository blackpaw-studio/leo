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

const secretQuestion = "migrate payroll-secret db to the new region?"

// raiseQuestion has agent "other" ask an AskUserQuestion whose summary (the
// bridge feed's reason detail) is secretQuestion.
func raiseQuestion(s *Server) {
	s.attention.RegisterToken("other-tok", "other")
	s.attention.SetByTokenNeedsInput("other-tok", observe.AttentionReason{
		Kind: observe.AttentionReasonQuestion, Tool: "AskUserQuestion", Detail: secretQuestion,
	})
}

// TestAskUserQuestionTextStaysOffNonOperatorStateAndStream: another agent's
// question text rides attention.reason.detail on agent_activity frames and
// the state snapshot. The agent token must see that the agent needs
// attention (kind, tool, state), never what it asked; operator and paired
// device still see the text.
func TestAskUserQuestionTextStaysOffNonOperatorStateAndStream(t *testing.T) {
	s, deviceToken, raiseAgain := attentionInputServer(t)
	svc := s.agentSvc.(*mockAgentService)
	svc.records = append(svc.records, agent.Record{Name: "other", Status: "running"})
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

			// The question and a sentinel activity frame behind it.
			s.attention.Remove("other")
			raiseQuestion(s)
			raiseAgain()
			var stream strings.Builder
			var sawQuestionFrame bool
			for {
				event, data := readSSEFrame(t, br)
				stream.WriteString(event + " " + data + "\n")
				if event == "agent_activity" && strings.Contains(data, `"agent":"other"`) {
					sawQuestionFrame = true
					for _, want := range []string{`"kind":"question"`, `"tool":"AskUserQuestion"`, `"state":"needs_input"`} {
						if !strings.Contains(data, want) {
							t.Errorf("frame lost %s: %s", want, data)
						}
					}
				}
				if event == "agent_activity" && strings.Contains(data, `"agent":"a"`) {
					break
				}
			}
			if !sawQuestionFrame {
				t.Fatalf("no agent_activity frame for the asking agent:\n%s", stream.String())
			}
			if got := strings.Contains(stream.String(), secretQuestion); got != tc.wantText {
				t.Errorf("stream carries question text = %v, want %v\n%s", got, tc.wantText, stream.String())
			}

			w := requestAs(t, s, http.MethodGet, "/api/v1/state", tc.token, "")
			if w.Code != http.StatusOK {
				t.Fatalf("state = %d %s", w.Code, w.Body)
			}
			body := w.Body.String()
			if got := strings.Contains(body, secretQuestion); got != tc.wantText {
				t.Errorf("state carries question text = %v, want %v\n%s", got, tc.wantText, body)
			}
			if !strings.Contains(body, `"kind":"question"`) {
				t.Errorf("state lost the attention kind: %s", body)
			}
		})
	}
}
