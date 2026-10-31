package observe

import (
	"encoding/json"
	"strings"
)

// namePrefix is the canonical prefix of a stored agent name (it doubles as
// the tmux session name). agent.DisplayName delegates here.
const namePrefix = "leo-"

// DisplayName strips the canonical leo- prefix for user-facing display. A
// name without the prefix is returned as-is.
func DisplayName(name string) string {
	return strings.TrimPrefix(name, namePrefix)
}

// MarshalJSON adds display_name (DisplayName(Name)) next to the raw name on
// every agent object, whichever emitter built it (state snapshot, local
// observe routes, spawned/renamed event payloads).
func (a Agent) MarshalJSON() ([]byte, error) {
	type plain Agent
	return json.Marshal(struct {
		plain
		DisplayName string `json:"display_name"`
	}{plain: plain(a), DisplayName: DisplayName(a.Name)})
}
