package daemon

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// A dropped Peer copy would silently turn every peer back into a plain
// client, so the receiver would render dionysus#scout instead of scout@dionysus.
func TestClientPolicies_CarriesPeerFlag(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		APIClients: map[string]config.APIClientConfig{
			"dionysus": {CanMessage: []string{"rocket"}, Peer: true},
			"scout":    {CanMessage: []string{"rocket"}},
		},
	}
	got := map[string]bool{}
	for _, p := range clientPolicies(cfg) {
		got[p.Name] = p.Peer
	}
	if len(got) != 2 || !got["dionysus"] || got["scout"] {
		t.Fatalf("peer flags = %v, want dionysus=true scout=false", got)
	}
}
