package consult

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

// dispatchBridgeKeyPrefix keeps dispatch bridge keys apart from agent names,
// which never contain a dot.
const dispatchBridgeKeyPrefix = "dispatch."

// DefaultBridgeSendTimeout bounds how long a follow-up to a bridged dispatch
// waits for the mod's ack. A dispatch only takes a follow-up while idle, so
// the ack is normally immediate; the bound sits under the 30s HTTP timeouts
// of the MCP client and web server that carry the request.
const DefaultBridgeSendTimeout = 20 * time.Second

// DispatchBridgeKey is the bridge key (LEO_BRIDGE_AGENT) a dispatch's claude
// connects under.
func DispatchBridgeKey(id string) string { return dispatchBridgeKeyPrefix + id }

// DispatchIDFromBridgeKey returns the dispatch id a bridge key belongs to.
func DispatchIDFromBridgeKey(key string) (string, bool) {
	id, ok := strings.CutPrefix(key, dispatchBridgeKeyPrefix)
	return id, ok && id != ""
}

// InteractiveBridge wires the claude mod bridge into interactive dispatch
// launches. The zero value (no Hub) launches everything the legacy way.
type InteractiveBridge struct {
	Hub      *bridge.Hub
	Launcher *bridgemod.Launcher
	// ConnectTimeout is how long a bridged launch waits for its mod to
	// connect before relaunching the legacy way (default
	// bridgemod.DefaultConnectTimeout).
	ConnectTimeout time.Duration
	// SendTimeout bounds a follow-up's wait for its ack (default
	// DefaultBridgeSendTimeout).
	SendTimeout time.Duration
}

func (b InteractiveBridge) enabled() bool { return b.Hub != nil && b.Launcher != nil }

func (b InteractiveBridge) connectTimeout() time.Duration {
	if b.ConnectTimeout > 0 {
		return b.ConnectTimeout
	}
	return bridgemod.DefaultConnectTimeout
}

func (b InteractiveBridge) sendTimeout() time.Duration {
	if b.SendTimeout > 0 {
		return b.SendTimeout
	}
	return DefaultBridgeSendTimeout
}

// bridgedDispatch is one dispatch launched with the leo-bridge mod, plus
// what it takes to relaunch it the legacy way if the mod never connects.
type bridgedDispatch struct {
	id, key, caller string
	// launch is the launch token its claude was handed; target the
	// generation of key opened for it.
	launch string
	target bridge.Target
	pane   string // "" until the launch returns it
	// respawn is the respawn-pane argv (sans pane) of the legacy relaunch.
	respawn []string
	// legacyPaste is whether a legacy relaunch needs the opening pasted
	// (it was too large for argv).
	legacyPaste bool
	fellBack    bool
	// ownsReports: the launch's mod said hello and its session has not
	// ended for good, so its reports, not the shell hooks', drive the
	// dispatch (see BridgeOwnsReports).
	ownsReports bool
}

// bridgeEnvKeys are the variables the leo-bridge mod reads. A bridged launch
// sets them; every other launch blanks them, so a dispatch opened in an
// agent's tmux session never inherits that agent's bridge key.
var bridgeEnvKeys = bridgemod.EnvKeys

// SetBridge wires the claude mod bridge into dispatch launches. Safe to call
// before the runtime is in use; the zero InteractiveBridge disables it.
func (r *TmuxInteractiveRuntime) SetBridge(b InteractiveBridge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bridge = b
}

func (r *TmuxInteractiveRuntime) bridgeWiring() InteractiveBridge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bridge
}

// BridgesOpening reports whether a launch of harnessName will try to carry
// its opening over the bridge, so claude submits it itself instead of the
// dispatcher pasting it.
func (r *TmuxInteractiveRuntime) BridgesOpening(ctx context.Context, harnessName string) bool {
	b := r.bridgeWiring()
	return harnessName == "claude" && b.enabled() && b.Launcher.Capable(ctx, claudeharness.Claude{}.Binary())
}

// planDispatchBridge decides whether dispatch id's claude launch loads the
// mod. ok is false for any harness but claude, without a bridge, or when the
// launcher says legacy.
func (r *TmuxInteractiveRuntime) planDispatchBridge(ctx context.Context, harnessName, binary, id string) (bridgemod.Plan, bool) {
	b := r.bridgeWiring()
	if harnessName != "claude" || !b.enabled() {
		return bridgemod.Plan{}, false
	}
	return b.Launcher.Plan(ctx, binary, DispatchBridgeKey(id))
}

// queueBridgedOpening registers dispatch d, opening a generation of its
// key, and queues its opening as the user's own prompt (under an id derived
// from the dispatch and the prompt), ahead of the launch, so the mod finds
// it waiting whenever it connects. false means the bridge could not take it
// and the launch should go legacy.
func (r *TmuxInteractiveRuntime) queueBridgedOpening(d *bridgedDispatch, prompt string) bool {
	b := r.bridgeWiring()
	target, err := b.Hub.Open(d.key, d.launch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: the leo bridge is unavailable (%v); launching without it\n", d.id, err)
		return false
	}
	d.target = target
	if prompt != "" {
		if _, err := b.Hub.EnqueueTo(target, bridge.Opening(d.key, prompt)); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: queueing the opening on the leo bridge failed (%v); launching without it\n", d.id, err)
			b.Hub.ForgetGen(target)
			return false
		}
	}
	r.mu.Lock()
	if r.bridged == nil {
		r.bridged = map[string]*bridgedDispatch{}
	}
	r.bridged[d.id] = d
	r.mu.Unlock()
	return true
}

// bridgeLaunched records the pane a bridged launch landed in.
func (r *TmuxInteractiveRuntime) bridgeLaunched(id, pane string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := r.bridged[id]; d != nil {
		d.pane = pane
	}
}

// releaseBridge forgets dispatch id's bridge: its stream ends and anything
// still queued is dropped.
func (r *TmuxInteractiveRuntime) releaseBridge(id string) {
	r.mu.Lock()
	d := r.bridged[id]
	delete(r.bridged, id)
	hub := r.bridge.Hub
	r.mu.Unlock()
	if d != nil && hub != nil {
		hub.ForgetGen(d.target)
	}
}

// bridgedByPane returns a copy of pane's bridged dispatch, if any.
func (r *TmuxInteractiveRuntime) bridgedByPane(pane string) (bridgedDispatch, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, d := range r.bridged {
		if d.pane == pane && pane != "" {
			return *d, true
		}
	}
	return bridgedDispatch{}, false
}

// liveBridge returns pane's bridged dispatch when the mod carries its turns
// right now: launched bridged, never fallen back, its launch's stream
// connected.
func (r *TmuxInteractiveRuntime) liveBridge(pane string) (bridgedDispatch, *bridge.Hub, bool) {
	d, ok := r.bridgedByPane(pane)
	hub := r.bridgeWiring().Hub
	if !ok || d.fellBack || hub == nil {
		return d, hub, false
	}
	live, connected := hub.Live(d.key)
	return d, hub, connected && live == d.target
}

// BridgeOwnsReports reports whether dispatch id's turn state comes from its
// bridge rather than the claude shell hooks. Both report the same moments,
// so exactly one may drive the dispatcher, and which one must not flip
// while the launch runs: the bridge owns them from its mod's hello until
// that launch's session ends for good (not a /clear or resume), or its
// generation is forgotten (released, or relaunched without the bridge).
// A gap between stream reconnects keeps them with the bridge: the mod's
// reports travel on their own and still arrive.
func (r *TmuxInteractiveRuntime) BridgeOwnsReports(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d := r.bridged[id]
	return d != nil && !d.fellBack && d.ownsReports
}

// claimReport folds ev, from dispatch id's bridge, into who owns its
// reports, and reports whether the bridge owns ev itself: a hello starts
// ownership, and a final session.end is the bridge's last report.
func (r *TmuxInteractiveRuntime) claimReport(id string, ev bridge.Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.bridged[id]
	if d == nil || d.fellBack || ev.Gen != d.target.Gen {
		return false
	}
	if ev.Name == bridge.ReportHello {
		d.ownsReports = true
	}
	owned := d.ownsReports
	if ev.Name == bridge.EventSessionEnd && bridge.IsFinalSessionEnd(ev.Reason) {
		d.ownsReports = false
	}
	return owned
}

// FrameMessage returns message as the dispatch in pane will receive it: a
// follow-up the bridge carries is prefixed with who sent it, since it
// arrives as a plugin message rather than typed input. The dispatcher
// records the framed text, which is what the turn's prompt will echo.
func (r *TmuxInteractiveRuntime) FrameMessage(pane, message string) string {
	d, _, live := r.liveBridge(pane)
	if !live {
		return message
	}
	from := d.caller
	if from == "" {
		from = "the orchestrator"
	}
	return bridge.Framed(from, message)
}

// AwaitOpening waits for a bridged launch's mod to connect. handled is false
// for a pane the bridge never carried (the caller delivers the opening as
// before). If the mod does not connect in time, the queued opening is
// dropped and the pane is relaunched the legacy way; paste then says
// whether that relaunch still needs the opening pasted.
func (r *TmuxInteractiveRuntime) AwaitOpening(ctx context.Context, pane string) (handled, paste bool, err error) {
	d, ok := r.bridgedByPane(pane)
	if !ok || d.fellBack {
		return false, false, nil
	}
	b := r.bridgeWiring()
	waitCtx, cancel := context.WithTimeout(ctx, b.connectTimeout())
	_, waitErr := b.Hub.WaitFor(waitCtx, d.key, func(st bridge.State) bool { return st.HasConnected(d.target) })
	cancel()
	switch {
	case waitErr == nil:
		return true, false, nil
	case ctx.Err() != nil:
		return true, false, ctx.Err()
	case errors.Is(waitErr, bridge.ErrClosed):
		return true, false, waitErr
	}
	if !b.Hub.ForgetGenUnlessEverConnected(d.target) {
		return true, false, nil // connected (if only for a moment), or released
	}
	if !r.markFellBack(d.id) {
		return true, false, nil // released meanwhile; nothing to relaunch
	}
	fmt.Fprintf(os.Stderr, "dispatch %s: leo bridge did not connect within %s; relaunching without it\n", d.id, b.connectTimeout())
	argv := append([]string{"respawn-pane", "-k", "-t", pane}, d.respawn...)
	if err := r.run(ctx, argv...); err != nil {
		return true, false, fmt.Errorf("relaunching without the leo bridge: %w", err)
	}
	return true, d.legacyPaste, nil
}

// markFellBack flags dispatch id as relaunched without the bridge, false if
// it is no longer registered.
func (r *TmuxInteractiveRuntime) markFellBack(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.bridged[id]
	if d == nil {
		return false
	}
	d.fellBack = true
	return true
}

// deliverOverBridge arms the turn, then sends text as a non-user deliver and
// waits up to timeout for the mod's ack. Once queued, a deliver unacked by
// then stays queued and will be submitted once claude is free, so that is
// reported as accepted (the turn stays open, matched by its text when it
// starts) rather than as a failure inviting a resend that would deliver it
// twice. The wait is detached from ctx for the same reason: the caller
// going away does not unqueue the deliver. A rejection or a lost bridge is
// an error; it never falls back to tmux, which could deliver twice. A
// dispatch has no next launch to carry a still-queued deliver to (it does
// not outlive its claude, nor its daemon), so one never seen to start when
// the dispatch ends is reported in its turn's result instead (see
// endedTurnText).
func deliverOverBridge(ctx context.Context, hub *bridge.Hub, d bridgedDispatch, timeout time.Duration, text string, arm func() error) error {
	if arm != nil {
		if err := arm(); err != nil {
			return err
		}
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	err := hub.SendTo(sendCtx, d.target, bridge.Deliver(text, false))
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, bridge.ErrAckTimeout) {
		fmt.Fprintf(os.Stderr, "dispatch %s: leo bridge has not acked the message within %s; it stays queued\n", d.id, timeout)
		return nil
	}
	return err
}

// BridgeReportSink is what the dispatch bridge subscriber drives; the
// Dispatcher is one.
type BridgeReportSink interface {
	Report(id string, hr HookReport) error
	ApplyBridgeUsage(id string, raw json.RawMessage)
}

// BridgeTokenSink is a BridgeReportSink that also takes each completed
// turn's own token counts (nil when the mod reported none); the Dispatcher
// is one. The subscriber hands them over ahead of the turn's session usage.
type BridgeTokenSink interface {
	ApplyBridgeTokens(id, eventID string, tokens *bridge.TurnTokens)
}

// DispatchBridgeSubscriber returns the hub subscriber that drives bridged
// dispatches' state: a hello hands a dispatch's reports to its bridge (see
// BridgeOwnsReports), then each turn or session event of it goes to the
// sink as the claude shell-hook report it stands in for
// (bridge.HookPayload), and a completed turn's session usage goes along
// with it. The sink only records state and never sends on the bridge, as a
// subscriber must not.
func (r *TmuxInteractiveRuntime) DispatchBridgeSubscriber(sink BridgeReportSink) bridge.Subscriber {
	return bridge.SubscriberFunc(func(ev bridge.Event) {
		id, ok := DispatchIDFromBridgeKey(ev.Agent)
		if !ok || !r.claimReport(id, ev) {
			return
		}
		eventID, payload, ok := bridge.HookPayload(ev)
		if !ok {
			return
		}
		if err := sink.Report(id, HookReport{EventID: eventID, Payload: payload}); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: applying bridge %s: %v\n", id, ev.Name, err)
		}
		if tokenSink, ok := sink.(BridgeTokenSink); ok && ev.Name == bridge.EventTurnComplete {
			tokenSink.ApplyBridgeTokens(id, ev.EventID, ev.Tokens)
		}
		if ev.Name == bridge.EventTurnComplete && len(ev.Usage) > 0 {
			sink.ApplyBridgeUsage(id, ev.Usage)
		}
	})
}
