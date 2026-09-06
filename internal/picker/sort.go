package picker

import (
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
)

type SortMode string

const (
	SortModeRecent SortMode = "recent"
	SortModeName   SortMode = "name"
	SortModeUptime SortMode = "uptime"
)

func SortRecent(agents []Agent, lastAttached map[string]time.Time) []Agent {
	return sortBy(agents, func(a, b Agent) bool {
		at, bt := lastAttached[rowKey(a.Host, a.Name)], lastAttached[rowKey(b.Host, b.Name)]
		if !at.IsZero() || !bt.IsZero() {
			if at.IsZero() {
				return false
			}
			if bt.IsZero() {
				return true
			}
			if !at.Equal(bt) {
				return at.After(bt)
			}
		}
		return nameBefore(a, b)
	})
}

func SortName(agents []Agent, _ map[string]time.Time) []Agent { return sortBy(agents, nameBefore) }

func SortUptime(agents []Agent, _ map[string]time.Time) []Agent {
	return sortBy(agents, func(a, b Agent) bool {
		if a.StartedAt.IsZero() != b.StartedAt.IsZero() {
			return !a.StartedAt.IsZero()
		}
		if !a.StartedAt.IsZero() && !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		ad, bd := agent.DisplayName(a.Name), agent.DisplayName(b.Name)
		if ad != bd {
			return ad < bd
		}
		return nameBefore(a, b)
	})
}

func sortBy(agents []Agent, before func(Agent, Agent) bool) []Agent {
	out := append([]Agent(nil), agents...)
	sort.SliceStable(out, func(i, j int) bool { return before(out[i], out[j]) })
	return out
}

func nameBefore(a, b Agent) bool {
	if a.Host != b.Host {
		if a.Host == LocalHost {
			return true
		}
		if b.Host == LocalHost {
			return false
		}
		return a.Host < b.Host
	}
	ad, bd := agent.DisplayName(a.Name), agent.DisplayName(b.Name)
	if ad != bd {
		return ad < bd
	}
	return a.Name < b.Name
}
