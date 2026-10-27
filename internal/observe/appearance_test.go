package observe

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// assertWire compares v's JSON with testdata/wire/<name>, the golden wire
// contract LeoKit's decoder tests are built from.
func assertWire(t *testing.T, name string, v any) {
	t.Helper()
	want, err := os.ReadFile("testdata/wire/" + name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var wantDoc, gotDoc any
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Fatalf("%s mismatch\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func TestFeaturesIncludeAgentAppearance(t *testing.T) {
	if !slices.Contains(Features(), FeatureAgentAppearance) || FeatureAgentAppearance != "agent_appearance" {
		t.Fatalf("Features() = %v, want agent_appearance", Features())
	}
}

func TestAgentAppearanceChangedWire(t *testing.T) {
	assertWire(t, "appearance-event.json", &AgentAppearanceChangedPayload{
		Meta:  Meta{Seq: 42, At: time.Date(2026, 10, 27, 14, 5, 0, 0, time.UTC)},
		Agent: "olympus",
		Appearance: &Appearance{
			Emoji: "🏛️", Color: "teal",
			EmojiSource: AppearanceSourceManual, ColorSource: AppearanceRuleSource(0),
		},
	})
	assertWire(t, "appearance-event-null.json", &AgentAppearanceChangedPayload{
		Meta:  Meta{Seq: 43, At: time.Date(2026, 10, 27, 14, 6, 0, 0, time.UTC)},
		Agent: "olympus",
	})
	if EventAgentAppearanceChanged != "agent_appearance_changed" {
		t.Fatalf("event type = %q", EventAgentAppearanceChanged)
	}
}

func TestAgentAppearanceOmittedWhenUnset(t *testing.T) {
	b, err := json.Marshal(Agent{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "appearance") {
		t.Fatalf("unset appearance on the wire: %s", b)
	}
}
