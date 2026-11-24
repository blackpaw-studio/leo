package consult

import (
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Recorded grants for callers exempt from share.readable_by: a human at the
// CLI (no caller) and api_clients.
const (
	grantNoCaller        = "caller:none"
	grantAPIClientPrefix = "api_client:"
)

// RootIdentity is who a dispatch tree's root caller is, for grant matching:
// its caller string ("task:<name>" for a oneshot task, else an agent name)
// and, for an agent, the template it was spawned from.
type RootIdentity struct {
	Caller   string
	Template string
}

// matchGrant returns the first readable_by entry, in list order, that grants
// root read access. A task caller matches only task:<name>; any other caller
// matches agent:<name> by exact name or template:<t> by its spawned-from
// template. Exempt callers (no caller, api clients) are granted without an
// entry and report the grant recorded for them.
func matchGrant(readableBy []string, root RootIdentity, apiClient bool) (string, bool) {
	if root.Caller == "" {
		return grantNoCaller, true
	}
	if apiClient {
		return grantAPIClientPrefix + root.Caller, true
	}
	task, isTask := strings.CutPrefix(root.Caller, "task:")
	for _, entry := range readableBy {
		kind, name, err := config.ParseGrant(entry)
		if err != nil {
			continue
		}
		switch {
		case isTask && kind == config.GrantTask && name == task,
			!isTask && kind == config.GrantAgent && name == root.Caller,
			!isTask && kind == config.GrantTemplate && root.Template != "" && name == root.Template:
			return entry, true
		}
	}
	return "", false
}
