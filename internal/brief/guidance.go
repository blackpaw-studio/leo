package brief

import (
	"fmt"
	"strings"
)

// Instruction is what an agent is told, once per session, about keeping its
// brief. The daemon owns the text; hooks only deliver it.
const Instruction = "leo: keep a short brief of your work with the leo_brief tool so the user can see where you are at a glance. " +
	"Pass goal (one line), status (current state or next step), decisions (up to 5 bullets) and open (up to 5 open items or what you are waiting on), " +
	"each at most 140 characters; every call replaces the whole brief. " +
	"Update it only when your goal, status or next step, decisions or open items change, not after every turn."

// NudgeText is the one-line reminder a prompt carries when the agent has not
// touched its brief for NudgeEveryTurns turns.
func NudgeText(hasBrief bool) string {
	if !hasBrief {
		return "leo: your Leo brief is missing. Call leo_brief with your goal, status, decisions and open items."
	}
	return "leo: your Leo brief may be stale. If your goal, status, decisions or open items changed, update it with leo_brief; otherwise ignore this."
}

// Restored is the context that gives an agent its own brief back after its
// context was rebuilt (source: clear, compact or resume).
func Restored(b Brief, source string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "leo: your current Leo brief, restored after %s (update it with leo_brief when it changes):", source)
	if b.Goal != "" {
		fmt.Fprintf(&sb, "\ngoal: %s", b.Goal)
	}
	if b.Status != "" {
		fmt.Fprintf(&sb, "\nstatus: %s", b.Status)
	}
	for _, list := range []struct {
		name  string
		items []string
	}{{"decisions", b.Decisions}, {"open", b.Open}} {
		if len(list.items) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "\n%s:", list.name)
		for _, item := range list.items {
			fmt.Fprintf(&sb, "\n- %s", item)
		}
	}
	return sb.String()
}
