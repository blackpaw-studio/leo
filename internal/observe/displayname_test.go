package observe

import (
	"encoding/json"
	"testing"
)

func TestAgentJSONCarriesDisplayNameAlongsideName(t *testing.T) {
	raw, err := json.Marshal(Agent{Name: "leo-home"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "leo-home" {
		t.Errorf("name = %v, want leo-home (must stay raw)", got["name"])
	}
	if got["display_name"] != "home" {
		t.Errorf("display_name = %v, want home", got["display_name"])
	}
}

func TestDisplayNameLeavesUnprefixedNamesAlone(t *testing.T) {
	if got := DisplayName("home"); got != "home" {
		t.Errorf("DisplayName(home) = %q", got)
	}
}
