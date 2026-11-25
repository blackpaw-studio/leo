package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// briefServer is a token-split server whose agent "alpha" (launch token
// "tok-a") has an agents.json record, with a bus to publish on. on sets
// brief.enabled in leo.yaml.
func briefServer(t *testing.T, on bool) (*Server, *observe.Bus, string) {
	t.Helper()
	s := newTokenSplitServer(t)
	if on {
		raw, err := os.ReadFile(s.configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.configPath, append(raw, "\nbrief:\n  enabled: true\n"...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if err := agentstore.Save(cfg.HomePath, agentstore.Record{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	s.attention = observe.NewAttentionStore(nil)
	s.attention.RegisterToken("tok-a", "alpha")
	s.attention.RegisterToken("tok-b", "beta")
	bus := observe.NewBus()
	s.publisher, s.events = bus, bus
	return s, bus, cfg.HomePath
}

func postBrief(t *testing.T, s *Server, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	return requestAs(t, s, http.MethodPost, "/api/agent/brief", bearer, body)
}

func TestBriefRouteStoresPublishesAndResetsTheNudgeCount(t *testing.T) {
	s, bus, home := briefServer(t, true)
	events, cancel, _ := bus.Subscribe(8)
	defer cancel()
	now := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := 0; i < brief.NudgeEveryTurns; i++ {
		s.briefs.TurnCompleted("alpha")
	}

	w := postBrief(t, s, testAgentToken, `{"token":"tok-a","brief":{"goal":" Ship it ","status":"testing","decisions":["atomic writes"],"open":["review"]}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var resp struct {
		OK    bool         `json:"ok"`
		Brief *brief.Brief `json:"brief"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Brief == nil || resp.Brief.Goal != "Ship it" {
		t.Fatalf("response = %s (%v)", w.Body, err)
	}
	stored := agentstore.BriefOf(home, "alpha")
	if stored == nil || stored.Status != "testing" || !stored.UpdatedAt.Equal(now) {
		t.Fatalf("stored = %+v", stored)
	}
	if agentstore.BriefOf(home, "beta") != nil {
		t.Fatal("another agent's brief was written")
	}
	select {
	case ev := <-events:
		p, ok := ev.Payload.(*observe.AgentBriefPayload)
		if ev.Type != observe.EventAgentBrief || !ok || p.Agent != "alpha" || p.Brief == nil || p.Brief.Goal != "Ship it" {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("no agent_brief event published")
	}
	select {
	case ev := <-events:
		t.Fatalf("extra event %+v", ev)
	default:
	}
	if s.briefs.Nudge("alpha") {
		t.Fatal("a write must restart the nudge count")
	}
}

func TestBriefRouteWithNoContentClearsAndPublishesNull(t *testing.T) {
	s, bus, home := briefServer(t, true)
	postBrief(t, s, testAgentToken, `{"token":"tok-a","brief":{"goal":"g"}}`)
	events, cancel, _ := bus.Subscribe(8)
	defer cancel()

	w := postBrief(t, s, testAgentToken, `{"token":"tok-a","brief":{}}`)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"brief":null`) {
		t.Fatalf("clear = %d %s", w.Code, w.Body)
	}
	if agentstore.BriefOf(home, "alpha") != nil {
		t.Fatal("brief not cleared")
	}
	ev := <-events
	if p, ok := ev.Payload.(*observe.AgentBriefPayload); !ok || p.Brief != nil {
		t.Fatalf("event = %+v, want a null brief", ev.Payload)
	}
}

func TestBriefRouteRefusals(t *testing.T) {
	s, _, home := briefServer(t, true)
	if w := postBrief(t, s, testAgentToken, `{"token":"nope","brief":{"goal":"g"}}`); w.Code != http.StatusForbidden {
		t.Errorf("unknown token = %d, want 403", w.Code)
	}
	w := postBrief(t, s, testAgentToken, `{"token":"tok-a","brief":{"goal":"`+strings.Repeat("x", 141)+`"}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "goal") {
		t.Errorf("over-length = %d %s, want 400 naming the field", w.Code, w.Body)
	}
	if agentstore.BriefOf(home, "alpha") != nil {
		t.Error("a refused write stored something")
	}
	if w := postBrief(t, s, testAgentToken, `{"token":"tok-a","brief":{"goal":"g","bogus":1}}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", w.Code)
	}
}

func TestBriefRouteIsRefusedWhileOff(t *testing.T) {
	s, bus, home := briefServer(t, false)
	events, cancel, _ := bus.Subscribe(8)
	defer cancel()

	w := postBrief(t, s, testAgentToken, `{"token":"tok-a","brief":{"goal":"g"}}`)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", w.Code, w.Body)
	}
	if agentstore.BriefOf(home, "alpha") != nil {
		t.Fatal("brief stored while off")
	}
	select {
	case ev := <-events:
		t.Fatalf("event published while off: %+v", ev)
	default:
	}
	s.SetBriefEnabled(true)
	if w := postBrief(t, s, testAgentToken, `{"token":"tok-a","brief":{"goal":"g"}}`); w.Code != http.StatusOK {
		t.Fatalf("after switching on = %d %s", w.Code, w.Body)
	}
}

// --- what readers see ---

const secretBrief = "migrating the payroll database"

func briefRecords() []agent.Record {
	return []agent.Record{{Name: "a", Status: "running", Brief: &brief.Brief{Goal: secretBrief, Status: "step 2", UpdatedAt: time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)}}}
}

func TestStateCarriesBriefsOnlyForOperatorClassReadersWhileOn(t *testing.T) {
	for _, on := range []bool{true, false} {
		s, deviceToken, _ := attentionInputServer(t)
		s.agentSvc.(*mockAgentService).records = briefRecords()
		if on {
			raw, _ := os.ReadFile(s.configPath)
			if err := os.WriteFile(s.configPath, append(raw, "\nbrief:\n  enabled: true\n"...), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			name, token string
			wantBrief   bool
		}{{"operator", testAPIToken, on}, {"paired device", deviceToken, on}, {"agent token", testAgentToken, false}} {
			w := requestAs(t, s, http.MethodGet, "/api/v1/state", tc.token, "")
			if w.Code != http.StatusOK {
				t.Fatalf("%s state = %d %s", tc.name, w.Code, w.Body)
			}
			if got := strings.Contains(w.Body.String(), secretBrief); got != tc.wantBrief {
				t.Errorf("enabled=%v %s: brief present = %v, want %v (%s)", on, tc.name, got, tc.wantBrief, w.Body)
			}
		}
	}
}

func TestEventsCarryBriefsOnlyForOperatorClassReaders(t *testing.T) {
	s, deviceToken, _ := attentionInputServer(t)
	bus := s.events.(*observe.Bus)
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	for _, tc := range []struct {
		name, token string
		want        bool
	}{{"operator", testAPIToken, true}, {"paired device", deviceToken, true}, {"agent token", testAgentToken, false}} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events", nil)
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
				t.Fatalf("first frame = %q", event)
			}

			bus.Publish(observe.Event{Type: observe.EventAgentBrief, Payload: &observe.AgentBriefPayload{Agent: "a", Brief: &brief.Brief{Goal: secretBrief}}})
			bus.Publish(observe.Event{Type: observe.EventBriefConfigChanged, Payload: &observe.BriefConfigChangedPayload{Enabled: true}})

			sawBrief, sawConfig := false, false
			for !sawConfig {
				event, data := readSSEFrame(t, br)
				switch event {
				case string(observe.EventAgentBrief):
					sawBrief = true
					if !strings.Contains(data, secretBrief) {
						t.Fatalf("frame = %s", data)
					}
				case string(observe.EventBriefConfigChanged):
					sawConfig = true
				}
			}
			if sawBrief != tc.want {
				t.Fatalf("agent_brief frame seen = %v, want %v", sawBrief, tc.want)
			}
		})
	}
}

// An agent's MCP server asks for the current switch on every event-stream
// (re)connection, so a flip it missed while disconnected is still seen.
func TestBriefConfigRouteReportsTheSwitchToTheLaunch(t *testing.T) {
	for _, on := range []bool{true, false} {
		s, _, _ := briefServer(t, on)
		w := requestAs(t, s, http.MethodPost, "/api/agent/brief/config", testAgentToken, `{"token":"tok-a"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("on=%v: status = %d, body = %s", on, w.Code, w.Body)
		}
		var resp struct {
			OK   bool `json:"ok"`
			Data struct {
				Enabled bool `json:"enabled"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Data.Enabled != on {
			t.Fatalf("on=%v: response = %s (%v)", on, w.Body, err)
		}
	}
}

func TestBriefConfigRouteFollowsAReload(t *testing.T) {
	s, _, _ := briefServer(t, false)
	s.SetBriefEnabled(true)
	w := requestAs(t, s, http.MethodPost, "/api/agent/brief/config", testAgentToken, `{"token":"tok-a"}`)
	if !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("body = %s, want the reloaded switch", w.Body)
	}
}

func TestBriefConfigRouteRefusesAnUnknownToken(t *testing.T) {
	s, _, _ := briefServer(t, true)
	if w := requestAs(t, s, http.MethodPost, "/api/agent/brief/config", testAgentToken, `{"token":"nope"}`); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
