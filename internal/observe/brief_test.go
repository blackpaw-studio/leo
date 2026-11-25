package observe

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/brief"
)

func TestAgentWithoutContentDropsTheBriefAndNeverMutates(t *testing.T) {
	in := Agent{Name: "a", Brief: &brief.Brief{Goal: "secret plan", Decisions: []string{"d"}}}
	out := in.WithoutContent()
	if out.Brief != nil {
		t.Fatalf("brief survived: %+v", out.Brief)
	}
	if in.Brief == nil || in.Brief.Goal != "secret plan" {
		t.Fatal("redaction mutated the original agent")
	}
}

func TestAgentBriefWireShape(t *testing.T) {
	at := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)
	b := &brief.Brief{Goal: "g", Status: "s", Decisions: []string{"d"}, Open: []string{"o"}, UpdatedAt: at}

	agent, _ := json.Marshal(Agent{Name: "a", Brief: b})
	var m map[string]json.RawMessage
	if err := json.Unmarshal(agent, &m); err != nil {
		t.Fatal(err)
	}
	want := `{"goal":"g","status":"s","decisions":["d"],"open":["o"],"updated_at":"2026-11-25T02:00:00Z"}`
	got := m["brief"]
	if string(got) != want {
		t.Errorf("agent brief = %s, want %s", got, want)
	}
	if _, has := m["brief"]; !has {
		t.Fatal("no brief key")
	}
	bare, _ := json.Marshal(Agent{Name: "a"})
	var bareMap map[string]any
	_ = json.Unmarshal(bare, &bareMap)
	if _, has := bareMap["brief"]; has {
		t.Errorf("an agent without a brief must omit the key: %s", bare)
	}

	ev := &AgentBriefPayload{Agent: "a", Brief: b}
	ev.stamp(88, at)
	evJSON, _ := json.Marshal(ev)
	wantEv := `{"seq":88,"at":"2026-11-25T02:00:00Z","agent":"a","brief":` + want + `}`
	if string(evJSON) != wantEv {
		t.Errorf("event = %s, want %s", evJSON, wantEv)
	}
	cleared, _ := json.Marshal(&AgentBriefPayload{Agent: "a"})
	if string(cleared) != `{"seq":0,"at":"0001-01-01T00:00:00Z","agent":"a","brief":null}` {
		t.Errorf("cleared event = %s, want an explicit null brief", cleared)
	}

	cfg, _ := json.Marshal(&BriefConfigChangedPayload{Enabled: false})
	if string(cfg) != `{"seq":0,"at":"0001-01-01T00:00:00Z","enabled":false}` {
		t.Errorf("config event = %s", cfg)
	}
}
