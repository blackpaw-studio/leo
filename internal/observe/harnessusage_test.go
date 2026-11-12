package observe

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func codexUsage(tokens int64) AgentUsage {
	return AgentUsage{
		SessionID:   "sid",
		Session:     UsageTotals{Tokens: tokens, CostUnknown: true},
		Incarnation: UsageTotals{Tokens: tokens, CostUnknown: true},
		Context:     &ContextUsage{Tokens: 10, Window: 100, Percent: 10},
		Model:       "gpt-test-1",
		RateLimits:  []RateLimit{{Kind: RateLimitFiveHour, PercentUsed: 5, ResetsAt: time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)}},
	}
}

func TestBridgeFeedHarnessUsage(t *testing.T) {
	t.Run("publishes once, and an identical repeat publishes nothing", func(t *testing.T) {
		h := newFeedHarness(t, nil)
		h.feed.SetHarnessUsage(map[string]AgentUsage{"bob": codexUsage(100)})
		evs := h.ofType(EventAgentUsage)
		if len(evs) != 1 {
			t.Fatalf("events = %d", len(evs))
		}
		if p := evs[0].Payload.(*AgentUsagePayload); p.Agent != "bob" || p.Usage.Session.Tokens != 100 {
			t.Fatalf("payload = %+v", p)
		}
		h.pub.reset()
		h.feed.SetHarnessUsage(map[string]AgentUsage{"bob": codexUsage(100)}) // equal, but freshly built pointers and slices
		if n := len(h.pub.snapshot()); n != 0 {
			t.Fatalf("published %d events for an unchanged map", n)
		}
		h.feed.SetHarnessUsage(map[string]AgentUsage{"bob": codexUsage(150)})
		if n := len(h.ofType(EventAgentUsage)); n != 1 {
			t.Fatalf("changed usage events = %d", n)
		}
	})

	t.Run("a removed agent is dropped silently", func(t *testing.T) {
		h := newFeedHarness(t, nil)
		h.feed.SetHarnessUsage(map[string]AgentUsage{"bob": codexUsage(100)})
		h.pub.reset()
		h.feed.SetHarnessUsage(nil)
		if n := len(h.pub.snapshot()); n != 0 {
			t.Fatalf("published %d events on removal", n)
		}
		if _, ok := h.feed.BridgeAgents()["bob"]; ok {
			t.Fatal("removed agent still read")
		}
	})

	t.Run("git rides along and a git change publishes", func(t *testing.T) {
		h := newFeedHarness(t, nil)
		h.feed.SetGitStats(map[string]GitStats{"bob": {Added: 2, Removed: 1}})
		if n := len(h.pub.snapshot()); n != 0 {
			t.Fatalf("git for an agent with no usage published %d events", n)
		}
		h.feed.SetHarnessUsage(map[string]AgentUsage{"bob": codexUsage(100)})
		evs := h.ofType(EventAgentUsage)
		if len(evs) != 1 || gitOf(evs[0]) == nil || *gitOf(evs[0]) != (GitStats{Added: 2, Removed: 1}) {
			t.Fatalf("first publish = %v", evs)
		}
		h.pub.reset()
		h.feed.SetGitStats(map[string]GitStats{"bob": {Added: 5}})
		evs = h.ofType(EventAgentUsage)
		if len(evs) != 1 || *gitOf(evs[0]) != (GitStats{Added: 5}) || evs[0].Payload.(*AgentUsagePayload).Usage.Session.Tokens != 100 {
			t.Fatalf("git change = %v", evs)
		}
	})

	t.Run("the state omits bridge and carries the usage without cost", func(t *testing.T) {
		h := newFeedHarness(t, nil)
		h.feed.SetHarnessUsage(map[string]AgentUsage{"bob": codexUsage(100)})
		bs, ok := h.feed.BridgeAgents()["bob"]
		if !ok || bs.Bridge != "" || bs.Usage == nil || bs.Usage.Session.Tokens != 100 {
			t.Fatalf("state = %+v ok=%v", bs, ok)
		}
		raw, err := json.Marshal(bs.Usage.Session)
		if err != nil || string(raw) != `{"tokens":100}` {
			t.Fatalf("session json = %s err=%v", raw, err)
		}
	})

	t.Run("bridged usage wins when an agent has both", func(t *testing.T) {
		h := newFeedHarness(t, nil)
		h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
		h.pub.reset()
		h.feed.SetHarnessUsage(map[string]AgentUsage{"alice": codexUsage(999)})
		if n := len(h.pub.snapshot()); n != 0 {
			t.Fatalf("harness usage published over bridged usage: %d events", n)
		}
		if u := h.usageNow(t); u.Session.Tokens == 999 || u.Session.CostUnknown {
			t.Fatalf("usage = %+v", u)
		}
	})
}
