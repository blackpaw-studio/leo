package observe

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// FeedClock is the bridge feed's time source: Now for throttle windows and
// AfterFunc for the trailing activity edge. stop reports whether it
// prevented the call.
type FeedClock interface {
	Now() time.Time
	AfterFunc(d time.Duration, fn func()) (stop func() bool)
}

type realFeedClock struct{}

func (realFeedClock) Now() time.Time { return time.Now() }

func (realFeedClock) AfterFunc(d time.Duration, fn func()) func() bool {
	return time.AfterFunc(d, fn).Stop
}

// KeyResolver maps a bridge key to the supervised agent it belongs to.
// ok=false for keys that are not agents (dispatch keys, unknown launches);
// the feed ignores their events.
type KeyResolver interface {
	AgentForKey(key string) (agent string, ok bool)
}

// KeyResolverFunc adapts a function to KeyResolver.
type KeyResolverFunc func(key string) (string, bool)

// AgentForKey calls f.
func (f KeyResolverFunc) AgentForKey(key string) (string, bool) { return f(key) }

// MaxUsageCostUSD caps any one cost the bridge feed accepts or accumulates;
// a reported total above it is treated as malformed.
const MaxUsageCostUSD = 1e6

// maxFeedSeenEvents bounds each agent's replay-dedup memory.
const maxFeedSeenEvents = 64

// BridgeFeed projects the claude mod bridge's events into the
// observability API: it is a bridge hub Subscriber that keeps each bridged
// agent's usage, subagents, attention reason and running tool (read through
// BridgeAgents), drives the attention store's turn transitions, and
// publishes the agent_turn_*, agent_session_ended, agent_compaction,
// agent_usage and (coalesced) agent_activity events. All methods are
// nil-safe and safe for concurrent use.
type BridgeFeed struct {
	resolve   KeyResolver
	publisher Publisher
	attention *AttentionStore
	activity  ActivityProvider
	clock     FeedClock
	connected func(key string) bool

	mu     sync.Mutex
	agents map[string]*feedAgent
	// names maps each bridge key to the agent its state is filed under,
	// so a rename moves the state instead of starting over.
	names map[string]string
}

// feedAgent is one agent's projection.
type feedAgent struct {
	key string
	gen uint64

	sessionID   string
	session     UsageTotals
	incarnation UsageTotals
	// sessionCost is the session's running cost as last reported, so a
	// turn's cost is the difference.
	sessionCost float64
	context     *ContextUsage
	hasUsage    bool

	subagents int
	reason    *AttentionReason
	action    *Action

	// seen holds digests, not the ids, so its size is fixed whatever the
	// mod sends.
	seen      map[[sha256.Size]byte]bool
	seenOrder [][sha256.Size]byte

	lastActivity time.Time
	stopTrailing func() bool
	// trailingGen identifies the armed trailing callback; a callback that
	// finds it moved on (an immediate edge published since) does nothing.
	trailingGen uint64
}

// BridgeFeedOption configures a BridgeFeed.
type BridgeFeedOption func(*BridgeFeed)

// WithFeedAttention wires the attention store the feed drives turn and
// needs_input transitions and subagent counts into.
func WithFeedAttention(s *AttentionStore) BridgeFeedOption {
	return func(f *BridgeFeed) { f.attention = s }
}

// WithFeedActivity wires the tracker whose activity reading rides along on
// the feed's agent_activity events.
func WithFeedActivity(p ActivityProvider) BridgeFeedOption {
	return func(f *BridgeFeed) { f.activity = p }
}

// WithFeedClock replaces the wall clock (tests).
func WithFeedClock(c FeedClock) BridgeFeedOption {
	return func(f *BridgeFeed) { f.clock = c }
}

// WithFeedConnected wires the check BridgeAgents reports each agent's
// bridge link from. Without it every agent reads as absent.
func WithFeedConnected(fn func(key string) bool) BridgeFeedOption {
	return func(f *BridgeFeed) { f.connected = fn }
}

// NewBridgeFeed builds a feed resolving bridge keys through resolve and
// publishing through publisher (nil publishes nothing).
func NewBridgeFeed(resolve KeyResolver, publisher Publisher, opts ...BridgeFeedOption) *BridgeFeed {
	f := &BridgeFeed{
		resolve:   resolve,
		publisher: publisher,
		clock:     realFeedClock{},
		agents:    map[string]*feedAgent{},
		names:     map[string]string{},
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// BridgeAgents returns each bridged agent's projected state.
func (f *BridgeFeed) BridgeAgents() map[string]BridgeAgentState {
	out := map[string]BridgeAgentState{}
	if f == nil {
		return out
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, st := range f.agents {
		bs := BridgeAgentState{Bridge: BridgeAbsent, Subagents: st.subagents, Reason: cloneOf(st.reason), CurrentAction: cloneOf(st.action)}
		if f.connected != nil && f.connected(st.key) {
			bs.Bridge = BridgeConnected
		}
		if st.hasUsage {
			u := st.usage()
			bs.Usage = &u
		}
		out[name] = bs
	}
	return out
}

// OnBridgeEvent folds one bridge event into the agent's projection.
func (f *BridgeFeed) OnBridgeEvent(ev bridge.Event) {
	if f == nil || f.resolve == nil {
		return
	}
	name, ok := f.resolve.AgentForKey(ev.Agent)
	if !ok || name == "" {
		return
	}
	if !bridge.ValidSessionID(ev.SessionID) {
		// Mod-supplied and published: never store or echo a malformed one.
		ev.SessionID = ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.agentLocked(ev.Agent, name)
	if ev.EventID != "" && st.replayed(sha256.Sum256([]byte(ev.Name+"\x00"+ev.EventID))) {
		return
	}
	usageChanged := st.observeLaunch(ev)
	switch ev.Name {
	case bridge.ReportHello:
		// A fresh mod (respawn or hot reload) counts its subagents from
		// zero; keeping the old count would hold the agent forever.
		f.setSubagentsLocked(name, st, 0)
	case bridge.EventTurnStart:
		f.publish(EventAgentTurnStarted, &AgentTurnStartedPayload{Agent: name, SessionID: ev.SessionID})
		f.attention.Advance(name, AttentionWorking)
	case bridge.EventTurnComplete:
		f.turnCompleteLocked(name, st, ev)
		return
	case bridge.EventSessionEnd:
		st.action, st.reason = nil, nil
		f.publish(EventAgentSessionEnded, &AgentSessionEndedPayload{Agent: name, SessionID: ev.SessionID, Reason: ClampDetail(ev.Reason)})
	case bridge.EventActivity:
		f.activityLocked(name, st, ev.Activity)
	case bridge.EventAttention:
		f.attentionLocked(name, st, ev.Attention)
	case bridge.EventSubagents:
		if ev.Subagents != nil {
			f.setSubagentsLocked(name, st, ev.Subagents.Running)
		}
	case bridge.EventCompact:
		f.compactLocked(name, st, ev.Compact)
	}
	if st.applyUsage(ev.Usage) || usageChanged {
		f.publish(EventAgentUsage, &AgentUsagePayload{Agent: name, Usage: st.usage()})
	}
}

// agentLocked returns the state filed for key, moving it to name if the
// key's agent was renamed.
func (f *BridgeFeed) agentLocked(key, name string) *feedAgent {
	if prev, ok := f.names[key]; ok && prev != name {
		if st, ok := f.agents[prev]; ok {
			delete(f.agents, prev)
			f.agents[name] = st
		}
	}
	f.names[key] = name
	st, ok := f.agents[name]
	if !ok {
		st = &feedAgent{key: key}
		f.agents[name] = st
	}
	st.key = key
	return st
}

func (f *BridgeFeed) turnCompleteLocked(name string, st *feedAgent, ev bridge.Event) {
	st.action, st.reason = nil, nil
	p := &AgentTurnCompletedPayload{Agent: name, SessionID: ev.SessionID, Outcome: TurnCompleted, Preview: ClampPreview(ev.Message)}
	if ev.Reason == "aborted" {
		p.Outcome = TurnAborted
	}
	if t := ev.Tokens; t != nil {
		p.Tokens = TurnTokens{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheCreation: t.CacheCreation}
		st.addTokens(addSaturating(addSaturating(t.Input, t.Output), addSaturating(t.CacheRead, t.CacheCreation)))
	}
	if len(ev.Usage) > 0 {
		_, cost, ok := st.foldUsage(ev.Usage)
		if ok {
			p.CostUSD = &cost
		}
		p.Context = cloneOf(st.context)
	}
	f.publish(EventAgentTurnCompleted, p)
	f.attention.Advance(name, AttentionFinished)
}

func (f *BridgeFeed) activityLocked(name string, st *feedAgent, r *bridge.ActivityReport) {
	if r == nil {
		return
	}
	st.action = nil
	if detail := strings.TrimSpace(r.Tool + " " + r.Summary); r.Tool != "" {
		st.action = &Action{Kind: ActionKindTool, Detail: ClampDetail(detail)}
	}
	f.throttleActivityLocked(name, st)
}

// throttleActivityLocked publishes agent_activity at most once per
// ActivityMinInterval per agent: at once when the last one is older, or
// else once the interval ends, carrying the latest reading then.
func (f *BridgeFeed) throttleActivityLocked(name string, st *feedAgent) {
	now := f.clock.Now()
	if st.lastActivity.IsZero() || now.Sub(st.lastActivity) >= ActivityMinInterval {
		st.cancelTrailing()
		f.publishActivityLocked(name, st, now)
		return
	}
	if st.stopTrailing != nil {
		return
	}
	st.trailingGen++
	gen := st.trailingGen
	st.stopTrailing = f.clock.AfterFunc(st.lastActivity.Add(ActivityMinInterval).Sub(now), func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if st.trailingGen != gen || st.stopTrailing == nil {
			return
		}
		// The agent may have been renamed since; publish under its
		// current name, or not at all if its state is gone.
		for current, s := range f.agents {
			if s == st {
				st.stopTrailing = nil
				f.publishActivityLocked(current, st, f.clock.Now())
				return
			}
		}
	})
}

// cancelTrailing disarms a pending trailing edge, including one whose
// callback is already waiting for the lock.
func (st *feedAgent) cancelTrailing() {
	if st.stopTrailing == nil {
		return
	}
	st.stopTrailing()
	st.stopTrailing = nil
	st.trailingGen++
}

func (f *BridgeFeed) publishActivityLocked(name string, st *feedAgent, now time.Time) {
	st.lastActivity = now
	if f.publisher == nil {
		return
	}
	reading := AgentActivity{Activity: ActivityUnknown}
	if f.activity != nil {
		if a, ok := f.activity.Activities()[name]; ok {
			reading = a
		}
	}
	p := &AgentActivityPayload{Agent: name, Activity: reading.Activity, CurrentAction: cloneOf(st.action)}
	if att, ok := f.attention.Get(name); ok {
		p.Attention = &att
	}
	f.publisher.Publish(Event{Type: EventAgentActivity, Payload: p})
}

func (f *BridgeFeed) attentionLocked(name string, st *feedAgent, r *bridge.AttentionReport) {
	if r == nil {
		return
	}
	if r.State != bridge.AttentionNeedsInput {
		st.reason = nil
		f.attention.Advance(name, AttentionWorking, AttentionNeedsInput)
		return
	}
	reason := ClampAttentionReason(AttentionReason{Kind: AttentionReasonKind(r.Kind), Tool: r.Tool, Detail: r.Summary})
	st.reason = &reason
	f.attention.AdvanceNeedsInput(name, reason)
}

func (f *BridgeFeed) setSubagentsLocked(name string, st *feedAgent, n int) {
	st.subagents = max(n, 0)
	f.attention.SetOutstandingSubagents(name, st.subagents)
}

func (f *BridgeFeed) compactLocked(name string, st *feedAgent, r *bridge.CompactReport) {
	if r == nil {
		return
	}
	p := &AgentCompactionPayload{Agent: name, Phase: CompactionPhase(r.Phase), Trigger: CompactionTrigger(r.Trigger)}
	if st.context != nil {
		pct := st.context.Percent
		p.ContextPercent = &pct
	}
	f.publish(EventAgentCompaction, p)
}

func (f *BridgeFeed) publish(typ EventType, p Payload) {
	if f.publisher != nil {
		f.publisher.Publish(Event{Type: typ, Payload: p})
	}
}

// replayed reports whether id was seen already, remembering it.
func (st *feedAgent) replayed(id [sha256.Size]byte) bool {
	if st.seen[id] {
		return true
	}
	if st.seen == nil {
		st.seen = map[[sha256.Size]byte]bool{}
	}
	st.seen[id] = true
	st.seenOrder = append(st.seenOrder, id)
	if len(st.seenOrder) > maxFeedSeenEvents {
		delete(st.seen, st.seenOrder[0])
		st.seenOrder = st.seenOrder[1:]
	}
	return false
}

// observeLaunch resets the incarnation totals on a new generation (a
// respawn) and the session totals on a new session id, reporting whether
// usage already reported changed.
func (st *feedAgent) observeLaunch(ev bridge.Event) bool {
	changed := false
	if ev.Gen != 0 && ev.Gen != st.gen {
		if st.gen != 0 {
			st.incarnation = UsageTotals{}
			changed = st.hasUsage
		}
		st.gen = ev.Gen
	}
	if ev.SessionID != "" && ev.SessionID != st.sessionID {
		if st.sessionID != "" {
			st.session, st.sessionCost, st.context = UsageTotals{}, 0, nil
			changed = changed || st.hasUsage
		}
		st.sessionID = ev.SessionID
	}
	return changed
}

func (st *feedAgent) addTokens(n int64) {
	st.hasUsage = true
	st.session.Tokens = addSaturating(st.session.Tokens, n)
	st.incarnation.Tokens = addSaturating(st.incarnation.Tokens, n)
}

// modUsage is the part of the mod's session usage ($.session.usage()) the
// feed reads: the session's running cost and the context window fill.
type modUsage struct {
	Cost *struct {
		USD *float64 `json:"usd"`
	} `json:"cost"`
	Context *struct {
		Tokens  *int64   `json:"tokens"`
		Window  *int64   `json:"window"`
		Percent *float64 `json:"percent"`
	} `json:"context"`
}

// applyUsage folds a session usage report in, reporting whether it changed
// anything.
func (st *feedAgent) applyUsage(raw json.RawMessage) bool {
	changed, _, _ := st.foldUsage(raw)
	return changed
}

// foldUsage folds a session usage report in. cost is what the session
// spent since the previous report, valid when hasCost. Malformed or
// out-of-range values are ignored.
func (st *feedAgent) foldUsage(raw json.RawMessage) (changed bool, cost float64, hasCost bool) {
	if len(raw) == 0 {
		return false, 0, false
	}
	var u modUsage
	if json.Unmarshal(raw, &u) != nil {
		return false, 0, false
	}
	if u.Cost != nil && u.Cost.USD != nil && validAmount(*u.Cost.USD) {
		total := *u.Cost.USD
		cost, hasCost = total-st.sessionCost, true
		if cost < 0 {
			// The running total went backwards: a new session the
			// feed did not see begin. Count it from zero.
			cost = total
		}
		if total != st.sessionCost {
			st.sessionCost = total
			st.session.CostUSD = total
			st.incarnation.CostUSD = min(st.incarnation.CostUSD+cost, MaxUsageCostUSD)
			changed = true
		}
	}
	if c := u.Context; c != nil && c.Tokens != nil && c.Window != nil && c.Percent != nil &&
		*c.Tokens >= 0 && *c.Window >= 0 && validAmount(*c.Percent) {
		next := ContextUsage{Tokens: *c.Tokens, Window: *c.Window, Percent: *c.Percent}
		if st.context == nil || *st.context != next {
			st.context = &next
			changed = true
		}
	}
	if changed {
		st.hasUsage = true
	}
	return changed, cost, hasCost
}

func (st *feedAgent) usage() AgentUsage {
	return AgentUsage{SessionID: st.sessionID, Session: st.session, Incarnation: st.incarnation, Context: cloneOf(st.context)}
}

// validAmount reports whether v is a finite amount in [0, MaxUsageCostUSD].
func validAmount(v float64) bool { return v >= 0 && v <= MaxUsageCostUSD }

func addSaturating(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func cloneOf[T any](p *T) *T {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}
