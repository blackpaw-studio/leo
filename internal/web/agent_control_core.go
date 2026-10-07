package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// The transports a control operation can take: the agent's live mod
// bridge, or the legacy path (peer inbox, tmux, or a non-claude
// SessionDriver).
const (
	transportBridge = "bridge"
	transportLegacy = "legacy"
)

// controlOutcome is how a control core settled, independent of the
// transport that asked: each handler renders it in its own shape. status
// is the HTTP status it maps to; a 202 means the operation was taken but
// has not happened yet. err is empty on success.
type controlOutcome struct {
	status    int
	transport string
	// bridgeQueued marks a 202 that is the bridge holding the command
	// behind a running turn (the /web routes report only this one as
	// "queued"; the wake-then-deliver 202 predates it and says nothing).
	bridgeQueued bool
	err          string
}

func controlOK(transport string) controlOutcome {
	return controlOutcome{status: http.StatusOK, transport: transport}
}

func controlFailed(status int, transport, format string, args ...any) controlOutcome {
	return controlOutcome{status: status, transport: transport, err: fmt.Sprintf(format, args...)}
}

// interruptAgent interrupts name's running turn: through the mod when its
// bridge is connected (no Escape burst, and a failure is reported rather
// than retried through tmux), otherwise a burst of Escapes into its pane,
// sent at once and repeated in the background for a few seconds to catch
// state transitions (e.g. a tool call that completes mid-interrupt and
// re-arms the input prompt).
func (s *Server) interruptAgent(ctx context.Context, name string) controlOutcome {
	if target, ok := s.bridgeRoute(name, "interrupt"); ok {
		ctx, cancel := context.WithTimeout(ctx, bridgeInterruptTimeout)
		defer cancel()
		if err := s.bridgeRouter.Hub.SendTo(ctx, target, bridge.Interrupt()); err != nil {
			return controlFailed(http.StatusBadGateway, transportBridge, "Interrupting %s failed: %v", name, err)
		}
		return controlOK(transportBridge)
	}
	sessionName := agent.SessionName(name)

	tmuxPath := findTmuxPath()
	pane := s.resolvePaneTarget(tmuxPath, sessionName)
	escArgs := tmux.Args("send-keys", "-t", pane, "Escape")
	// Send Escape immediately, then keep sending to catch state transitions.
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	// Also send delayed Escapes in background to catch tool completions. This
	// spans up to ~2.5s, long enough for a crash-restart to tear down and
	// recreate the session mid-burst — re-resolve the pane before each
	// delayed send rather than reusing the request-entry resolution, or a
	// dead pane ID silently no-ops for the rest of the burst.
	go func() {
		for i := 0; i < interruptDelayedAttempts; i++ {
			time.Sleep(interruptDelayedPoll)
			delayedPane := s.resolvePaneTarget(tmuxPath, sessionName)
			delayedArgs := tmux.Args("send-keys", "-t", delayedPane, "Escape")
			s.execCommand(tmuxPath, delayedArgs...).Run() //nolint:errcheck
		}
		if s.afterInterruptBurst != nil {
			s.afterInterruptBurst()
		}
	}()
	return controlOK(transportLegacy)
}

// slashCommandAgent runs /<verb> (with optional instructions after it) in
// an agent's claude. Over a live bridge the mod runs it once the current
// turn ends, so it is queued and answered 202 (an agent asks for this on
// itself, mid-turn); otherwise the command is typed into the pane, which
// interrupts the turn.
func (s *Server) slashCommandAgent(name, verb, instructions string, cmd bridge.Command) controlOutcome {
	if target, ok := s.bridgeRoute(name, verb); ok {
		accepted, err := s.bridgeSend(target, verb+" of "+name, cmd, bridgeControlWait, nil)
		switch {
		case err != nil:
			return controlFailed(http.StatusInternalServerError, transportBridge, "%s: %v", verb, err)
		case !accepted:
			return controlOutcome{status: http.StatusAccepted, transport: transportBridge, bridgeQueued: true}
		default:
			return controlOK(transportBridge)
		}
	}
	if err := s.typeSlashCommand(agent.SessionName(name), verb, instructions); err != nil {
		return controlFailed(http.StatusInternalServerError, transportLegacy, "%s", err.Error())
	}
	return controlOK(transportLegacy)
}

// typeSlashCommand types /<verb> key by key, so claude's slash menu
// engages, then any instructions as one literal paste — collapsed onto a
// single line, since a typed newline would submit early — then Enter.
func (s *Server) typeSlashCommand(sessionName, verb, instructions string) error {
	if err := s.typeKeys(sessionName, []string{"/" + verb}); err != nil {
		return err
	}
	if line := strings.Join(strings.Fields(stripControl(instructions, false)), " "); line != "" {
		tmuxPath := findTmuxPath()
		pane := s.resolvePaneTarget(tmuxPath, sessionName)
		if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, "-l", " "+line)...).Run(); err != nil {
			return fmt.Errorf("send-keys failed: %w", err)
		}
	}
	return s.typeKeys(sessionName, []string{"Enter"})
}

// messageAgent delivers text from from (empty for a human) into agent
// name's live prompt and submits it. Non-claude targets go through their
// SessionDriver; a dormant wake-on-message agent is started and delivered
// to in the background (202); a bridged agent takes it through its mod;
// everything else tries the peer inbox, then pastes into tmux.
func (s *Server) messageAgent(ctx context.Context, name, from, text string) controlOutcome {
	// Resolve the target's harness FIRST, before any tmux-touching logic.
	// Claude targets (harnessName == "" from an unresolved/claude target)
	// fall straight through to the fast-path / dormant-wake-then-deliver
	// logic below. A resolved non-claude target is routed to its
	// SessionDriver and returns immediately — it never touches tmux, and
	// never goes dormant (sweep skips non-claude records), so there is no
	// wake-then-deliver branch to consider for it.
	if harnessName, handle, ok := s.resolveMessageTarget(name); ok && harnessName != "" && harnessName != "claude" {
		out := s.dispatchNonClaudeMessage(harnessName, handle, stripControl(text, true))
		if out.err == "" {
			s.publishAgentMessage(from, name)
		}
		return out
	}

	// Validate the target against running sessions (agents). If the agent is
	// not live but is dormant with WakeOnMessage=true (idle-swept), start it
	// first and deliver via the readiness-probing path (InjectPrompt) — a
	// just-started claude takes tens of seconds to boot before its input box
	// accepts input, so the 2s fast-path below would silently drop the
	// message. A dormant agent with WakeOnMessage=false (a plain
	// operator-initiated stop) must NOT be woken this way — that is the whole
	// point of the flag — so it falls through to the same "no such agent"
	// response an unknown name gets.
	//
	// NOTE: a concurrent idle sweep can race here and make the live send-keys
	// path 500; the sender retries and auto-wakes again.
	states := s.processStates()
	if _, ok := states[name]; !ok {
		if s.agentSvc != nil && s.agentSvc.Wakeable(name) {
			return s.wakeAndDeliver(ctx, name, from, text)
		}
		names := make([]string, 0, len(states))
		for n := range states {
			names = append(names, n)
		}
		sort.Strings(names)
		return controlFailed(http.StatusNotFound, transportLegacy, "no such agent %q; running: %s", name, strings.Join(names, ", "))
	}

	if target, ok := s.bridgeRoute(name, "message"); ok {
		return s.deliverAgentMessageOverBridge(target, name, from, text)
	}
	return s.deliverAgentMessageLegacy(ctx, name, from, text)
}

// wakeAndDeliver starts dormant agent name and delivers text once it is
// ready. A cold-booting claude can take ~60s to load plugins/MCP before its
// input box accepts input — longer than the server's WriteTimeout — and the
// readiness-probing injector blocks for that whole window. Delivery runs
// asynchronously on a detached context (the caller's is cancelled once it
// answers) and the caller answers 202 now, so it isn't held on the
// connection and won't false-timeout and retry into a duplicate message.
func (s *Server) wakeAndDeliver(ctx context.Context, name, from, text string) controlOutcome {
	if err := s.agentSvc.Start(name); err != nil {
		return controlFailed(http.StatusInternalServerError, transportLegacy, "starting agent: %v", err)
	}
	const wakeDeliverTimeout = 3 * time.Minute
	sessionName := agent.SessionName(name)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wakeDeliverTimeout)
		defer cancel()
		if err := s.injectPrompt(ctx, sessionName, stripControl(text, true)); err != nil {
			// #nosec G706 -- name matched an existing agentstore record
			// (Wakeable returned true), so it is a validated identifier,
			// not raw request input; no control chars can reach the log.
			log.Printf("web: async message delivery after start of %q failed: %v", sessionName, err)
			return
		}
		// Announced from inside the goroutine, once delivery actually
		// succeeded — not at 202-accept time. The 202 only means the
		// message was queued; this cold-boot path can still fail minutes
		// later, and announcing early would tell a consumer two agents were
		// talking when nothing was ever delivered. Every delivery path
		// therefore announces on delivery, never on acceptance.
		s.publishAgentMessage(from, name)
	}()
	return controlOutcome{status: http.StatusAccepted, transport: transportLegacy}
}

// deliverAgentMessageLegacy is the live (already-running) fast path: the
// peer inbox, else a literal paste + readiness confirmation + Enter.
func (s *Server) deliverAgentMessageLegacy(ctx context.Context, name, from, text string) controlOutcome {
	sessionName := agent.SessionName(name)
	if socketPath, err := s.resolvePeerSocket(ctx, sessionName); err == nil {
		if err := s.deliverPeer(ctx, socketPath, text); err == nil {
			s.publishAgentMessage(from, name)
			return controlOK(transportLegacy)
		} else {
			log.Printf("web: peer inbox delivery to %s failed: %s; falling back to tmux", strconv.Quote(sessionName), strconv.Quote(err.Error()))
		}
	} else {
		log.Printf("web: peer inbox socket resolution for %s failed: %s; falling back to tmux", strconv.Quote(sessionName), strconv.Quote(err.Error()))
	}

	// Hold the session's input as every paste into it does, so no other
	// delivery's text or probe interleaves with this one's; but wait no
	// longer than sessionInputWait, and type nothing once the client is
	// gone (the lock's wait may win a race with its leaving): it would
	// arrive after the client gave up, beside the retry it sends.
	lockCtx, cancelLock := context.WithTimeout(ctx, sessionInputWait)
	unlock, err := tmux.LockSessionInput(lockCtx, sessionName)
	cancelLock()
	if err != nil {
		return controlFailed(http.StatusServiceUnavailable, transportLegacy, "agent %s is busy: another delivery into it has not finished (%v); try again", name, err)
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return controlFailed(http.StatusServiceUnavailable, transportLegacy, "the request ended before agent %s was free: %v", name, err)
	}

	tmuxPath := findTmuxPath()
	pane := s.resolvePaneTarget(tmuxPath, sessionName)

	// Literal paste of the message body.
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, "-l", stripControl(text, true))...).Run(); err != nil {
		return controlFailed(http.StatusInternalServerError, transportLegacy, "send message failed: %v", err)
	}

	// Wait until the input box reflects the typed text before submitting.
	// Claude's Ink REPL batches stdin; an Enter that lands in the same input
	// burst as the literal text is treated as a newline, not a submit, leaving
	// the message unsent (the intermittent "Enter not registered" bug).
	// Confirming the text rendered forces Enter to arrive as a discrete
	// keypress. Bounded, and falls open if the pane never confirms (busy
	// mid-turn or unreadable) so a message is never silently dropped.
	s.waitForInputContent(tmuxPath, pane)

	// Separate Enter to submit.
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", pane, "Enter")...).Run(); err != nil {
		return controlFailed(http.StatusInternalServerError, transportLegacy, "submit message failed: %v", err)
	}

	s.publishAgentMessage(from, name)
	return controlOK(transportLegacy)
}

// dispatchNonClaudeMessage delivers text to a non-claude session via its
// SessionDriver's Inject and never touches tmux. The caller announces the
// message only when the outcome carries no error — a failed send must
// announce nothing.
func (s *Server) dispatchNonClaudeMessage(harnessName string, h harness.SessionHandle, text string) controlOutcome {
	hd, err := harness.Get(harnessName)
	if err != nil {
		return controlFailed(http.StatusInternalServerError, transportLegacy, "resolving harness %q: %v", harnessName, err)
	}
	drv := hd.Driver()
	if drv == nil {
		return controlFailed(http.StatusInternalServerError, transportLegacy, "harness %q has no session driver", harnessName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), nonClaudeInjectTimeout)
	defer cancel()
	if _, err := drv.Inject(ctx, h, text); err != nil {
		return controlFailed(http.StatusInternalServerError, transportLegacy, "delivering message: %v", err)
	}
	return controlOK(transportLegacy)
}

// processStates is the running process table, empty when the server has
// no process provider.
func (s *Server) processStates() map[string]ProcessStateInfo {
	if s.processes == nil {
		return map[string]ProcessStateInfo{}
	}
	return s.processes.States()
}

// stripControl drops the characters a terminal reads as keystrokes or
// sequences rather than text: C0 controls, DEL and C1 controls. With
// keepLayout, newline and tab survive (a message keeps its lines);
// without, they fold to spaces. Text typed or pasted into tmux goes
// through it, so a body cannot carry a Ctrl-C or end a bracketed paste.
func stripControl(text string, keepLayout bool) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			if keepLayout {
				return r
			}
			return ' '
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, text)
}
