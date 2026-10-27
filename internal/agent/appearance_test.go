package agent

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// appearanceVectors is testdata/appearance-vectors.json, the resolution cases
// LeoKit's resolver runs too (its copy must stay byte-identical).
type appearanceVectors struct {
	Version int `json:"version"`
	Cases   []struct {
		Name  string `json:"name"`
		Agent struct {
			Name         string   `json:"name"`
			Template     string   `json:"template"`
			Harness      string   `json:"harness"`
			Environments []string `json:"environments"`
		} `json:"agent"`
		Manual   *agentstore.Appearance    `json:"manual"`
		Rules    config.AppearanceRuleList `json:"rules"`
		Expected *observe.Appearance       `json:"expected"`
	} `json:"cases"`
}

func TestResolveAppearance(t *testing.T) {
	data, err := os.ReadFile("testdata/appearance-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors appearanceVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if vectors.Version != 1 || len(vectors.Cases) == 0 {
		t.Fatalf("unexpected vectors file: version %d, %d cases", vectors.Version, len(vectors.Cases))
	}
	for _, tc := range vectors.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			facts := AppearanceFacts{
				Name: tc.Agent.Name, Template: tc.Agent.Template,
				Harness: tc.Agent.Harness, Environments: tc.Agent.Environments,
			}
			got := ResolveAppearance(tc.Rules, facts, tc.Manual)
			if !reflect.DeepEqual(got, tc.Expected) {
				t.Fatalf("ResolveAppearance = %s, want %s", appearanceJSON(t, got), appearanceJSON(t, tc.Expected))
			}
		})
	}
}

func appearanceJSON(t *testing.T, a *observe.Appearance) string {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestResolveAppearanceIgnoresEmptyManual(t *testing.T) {
	if got := ResolveAppearance(nil, AppearanceFacts{Name: "a"}, &agentstore.Appearance{}); got != nil {
		t.Fatalf("an empty manual appearance resolved to %+v, want nil", got)
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "", true},
		{"*", "anything/with/slashes", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"?", "é", true},
		{"??", "é", false},
		{"*-dj", "alfred-itunes-dj", true},
		{"alfred-*", "my-alfred-x", false},
		{"a**b", "ab", true},
		{"a*", "a*b", true},
		{"a?", "a*", true},
	}
	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.s); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}
