package observe

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFeaturesIncludeAgentMove(t *testing.T) {
	if !slices.Contains(Features(), FeatureAgentMove) || FeatureAgentMove != "agent_move" {
		t.Fatalf("Features() = %v, want agent_move", Features())
	}
}

func TestAgentMovedAtWire(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)
	b, err := json.Marshal(Agent{MovedTo: "laptop", MovedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"moved_to":"laptop","moved_at":"2026-10-28T09:30:00Z"`) {
		t.Fatalf("agent JSON = %s", b)
	}
	b, err = json.Marshal(Agent{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "moved_at") {
		t.Fatalf("unmoved agent carries moved_at: %s", b)
	}
}
