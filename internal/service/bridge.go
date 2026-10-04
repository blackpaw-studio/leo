package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// DefaultBridgeConnectTimeout is how long a bridged claude launch with an
// opening prompt waits for its mod to connect before the agent is relaunched
// the legacy way.
const DefaultBridgeConnectTimeout = bridgemod.DefaultConnectTimeout

// bridgeEnvKeys are the variables the leo-bridge mod reads. leo owns them:
// a bridged launch sets them and every other launch blanks them.
var bridgeEnvKeys = bridgemod.EnvKeys

// pasteOpeningPrompt delivers an opening prompt too large for argv to a
// legacy claude by tmux paste. A package var so tests can observe it.
var pasteOpeningPrompt = tmux.InjectPrompt

// supervisorBridge is the claude mod bridge as the supervisor uses it.
type supervisorBridge struct {
	hub            *bridge.Hub
	launcher       *bridgemod.Launcher
	connectTimeout time.Duration
}

// SetBridge wires the claude mod bridge into claude launches: a launcher
// that plans bridged launches, the hub their mods connect to, and how long a
// launch with an opening prompt waits for that connection (<= 0 means
// DefaultBridgeConnectTimeout). A nil hub leaves every launch legacy; a nil
// launcher still lets stop/reset forget bridge state.
func (s *Supervisor) SetBridge(hub *bridge.Hub, launcher *bridgemod.Launcher, connectTimeout time.Duration) {
	if connectTimeout <= 0 {
		connectTimeout = DefaultBridgeConnectTimeout
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if hub == nil {
		s.bridge = nil
		return
	}
	s.bridge = &supervisorBridge{hub: hub, launcher: launcher, connectTimeout: connectTimeout}
}

func (s *Supervisor) bridgeWiring() *supervisorBridge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bridge
}

// BridgeKey returns the bridge key of name's live launch, if it loaded the
// bridge.
func (s *Supervisor) BridgeKey(name string) (string, bool) {
	s.mu.RLock()
	id, ok := s.identities[name]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	key := id.BridgeKey()
	return key, key != ""
}

// BridgeRouteForSession returns how the launch of the agent running in tmux
// session uses the bridge. A session leo does not supervise has nothing to
// wait for: a planned legacy route.
func (s *Supervisor) BridgeRouteForSession(session string) BridgeRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.identities {
		if id.SessionName() == session {
			return id.BridgeRoute()
		}
	}
	return BridgeRoute{Planned: true}
}

// BridgeState returns name's bridge state while its mod is connected.
func (s *Supervisor) BridgeState(name string) (bridge.State, bool) {
	w := s.bridgeWiring()
	key, ok := s.BridgeKey(name)
	if w == nil || !ok {
		return bridge.State{}, false
	}
	st := w.hub.State(key)
	return st, st.Connected
}

// BridgeStatus is name's leo bridge as the agent list shows it:
// agent.BridgeConnected while its mod is connected, agent.BridgeAbsent for
// any other live claude agent (launched legacy, or its mod gone), with the
// commands queued for it while absent, and empty for other harnesses and
// unknown names.
func (s *Supervisor) BridgeStatus(name string) agent.BridgeStatus {
	s.mu.RLock()
	id, ok := s.identities[name]
	s.mu.RUnlock()
	if !ok || (id.harness != "" && id.harness != "claude") {
		return agent.BridgeStatus{}
	}
	st, live := s.BridgeState(name)
	if live {
		return agent.BridgeStatus{State: agent.BridgeConnected}
	}
	return agent.BridgeStatus{State: agent.BridgeAbsent, Pending: st.Pending}
}

// BridgeRouter routes agent names to their live bridges; nil without a hub.
func (s *Supervisor) BridgeRouter() *bridge.Router {
	w := s.bridgeWiring()
	if w == nil {
		return nil
	}
	return &bridge.Router{Hub: w.hub, Targets: s.bridgeTarget}
}

// BridgeCapable reports whether a claude agent launched now would load the
// bridge, so its opening prompt need not fit on argv.
func (s *Supervisor) BridgeCapable() bool {
	w := s.bridgeWiring()
	return w != nil && w.launcher != nil && w.launcher.Capable(s.ctx, s.claudePath)
}

// allocBridgeKey picks the key name's launch connects under: the name
// itself, unless another live identity's claude (renamed away since its
// launch) still holds it, or a surviving session another agent is about
// to adopt connects under it, then name.<random>. Dots never appear in agent
// names, so a suffixed key cannot collide with a plain one. A relaunch of
// the same identity keeps its key.
func (s *Supervisor) allocBridgeKey(name string, id *procIdentity) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if own := id.BridgeKey(); own != "" && !s.bridgeKeyTakenLocked(own, id) {
		return own
	}
	if !s.bridgeKeyTakenLocked(name, id) {
		return name
	}
	for {
		var b [3]byte
		_, _ = rand.Read(b[:])
		key := name + "." + hex.EncodeToString(b[:])
		if !s.bridgeKeyTakenLocked(key, id) {
			return key
		}
	}
}

// bridgeKeyTakenLocked reports whether key is another agent's: held by
// another identity's launch, or reserved for another agent's adoption.
// Caller holds s.mu.
func (s *Supervisor) bridgeKeyTakenLocked(key string, self *procIdentity) bool {
	if owner, ok := s.adoptionKeys[key]; ok && owner != self.Name() {
		return true
	}
	return s.bridgeKeyHeldLocked(key, self)
}

// bridgeKeyHeldLocked reports whether another identity's launch holds key.
// Caller holds s.mu.
func (s *Supervisor) bridgeKeyHeldLocked(key string, self *procIdentity) bool {
	for _, other := range s.identities {
		if other != self && other.BridgeKey() == key {
			return true
		}
	}
	return false
}

// forgetBridge drops the hub state of id's launch, if it had any.
func (s *Supervisor) forgetBridge(id *procIdentity) {
	w := s.bridgeWiring()
	if w == nil || id == nil {
		return
	}
	if target := id.BridgeRoute().Target; target.Key != "" {
		w.hub.ForgetGen(target)
	}
}

// tmuxSessionBridge reads the bridge key and launch token a running
// session was launched with from its environment: what its claude's mod
// connects and reports under. ok is false when either is unset or unusable.
func tmuxSessionBridge(tmuxPath, session string) (key, launch string, ok bool) {
	key, ok = tmuxSessionEnv(tmuxPath, session, bridgemod.EnvAgent)
	if !ok || !config.ValidName(key) {
		return "", "", false
	}
	launch, ok = tmuxSessionEnv(tmuxPath, session, bridgemod.EnvLaunch)
	if !ok || launch == "" {
		return "", "", false
	}
	return key, launch, true
}

// tmuxSessionEnv reads name from session's environment.
func tmuxSessionEnv(tmuxPath, session, name string) (string, bool) {
	out, err := exec.Command(tmuxPath, tmux.Args("show-environment", "-t", tmux.Target(session), name)...).Output() // #nosec G204 -- fixed tmux argv
	if err != nil {
		return "", false
	}
	return strings.CutPrefix(strings.TrimSpace(string(out)), name+"=")
}

// bridgeLaunch is how one claude launch (or adopted session) uses the
// bridge. The zero value is a legacy launch.
type bridgeLaunch struct {
	plan    bridgemod.Plan
	bridged bool
	// adopted: a live session a previous daemon launched. It is never
	// killed or relaunched for the bridge's sake.
	adopted bool
	// target is the generation of the bridge key opened for this launch.
	target bridge.Target
	// conversation is the one the launch's args name ("" for a fresh one
	// nobody knows the id of yet).
	conversation string
	// opening is the opening prompt the bridge delivers as this launch's
	// first user prompt ("" for none); the launch then omits it from argv.
	// ticket settles with its ack.
	opening string
	ticket  *bridge.Ticket
}

// planBridgeLaunch decides how this launch of id, into conversation, uses
// the bridge, opening a new generation of its key bound to the launch's
// fresh token for a bridged one, and records the outcome on id.
func (s *Supervisor) planBridgeLaunch(ctx context.Context, claudePath, harnessName string, id *procIdentity, forceLegacy bool, conversation string) bridgeLaunch {
	w := s.bridgeWiring()
	if w == nil || w.launcher == nil || forceLegacy || harnessName != "claude" {
		id.setLegacy()
		return bridgeLaunch{}
	}
	plan, ok := w.launcher.Plan(ctx, claudePath, s.allocBridgeKey(id.Name(), id))
	if !ok {
		id.setLegacy()
		return bridgeLaunch{}
	}
	target, err := w.hub.Open(plan.Key, plan.Launch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: leo bridge unavailable (%v); launching without it\n", id.Name(), err)
		id.setLegacy()
		return bridgeLaunch{}
	}
	id.setBridge(target)
	return bridgeLaunch{plan: plan, bridged: true, target: target, conversation: conversation}
}

// endBridgedLaunch retires a bridged launch that is over: it reports
// whether the launch's mod ever connected, forgets the launch's generation
// (so nothing it left queued reaches the next launch, and its late reports
// are refused) and settles its opening's ack.
func (s *Supervisor) endBridgedLaunch(bl bridgeLaunch, opening *openingDelivery) (connected bool) {
	w := s.bridgeWiring()
	if !bl.bridged || w == nil {
		return false
	}
	connected = w.hub.State(bl.target.Key).HasConnected(bl.target)
	w.hub.ForgetGen(bl.target)
	opening.settle(bl)
	return connected
}

// bridgeLaunchSpec applies a launch plan: the bridged launch's args (with
// --plugin-dir, launch-only) and env, and where the claude opening brief
// goes. The brief leaves argv when the bridge delivers it, when it was
// already handled by an earlier launch, or when it is too large for argv,
// in which case pasteBrief names it for a paste once the session is up.
func bridgeLaunchSpec(bl bridgeLaunch, args []string, spec ProcessSpec, openingHandled bool) (launchArgs []string, launchSpec ProcessSpec, pasteBrief string) {
	launchArgs, launchSpec = args, spec
	launchSpec.bridgeEnv = nil
	if bl.bridged {
		launchArgs = bl.plan.Args(args)
		launchSpec.bridgeEnv = bl.plan.Env
	}
	if bl.opening != "" || openingHandled {
		launchSpec.OpeningBriefPath = ""
	}
	if path := launchSpec.OpeningBriefPath; path != "" && briefExceedsArgv(path) {
		launchSpec.OpeningBriefPath = ""
		pasteBrief = path
	}
	return launchArgs, launchSpec, pasteBrief
}

// bridgedSessionWritten reports whether claude has written the transcript of
// session id for workspace cwd. A test seam.
var bridgedSessionWritten = func(cwd, id string) bool {
	path, err := session.JSONLPath(cwd, id)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// argsAfterBridgeFallback returns the args for the legacy relaunch that
// follows a bridged launch whose mod never connected. That claude ran under
// --session-id <id> and may already have written the session's transcript
// (startup hook output, the mod's own log lines); claude refuses a
// --session-id whose transcript exists, so the relaunch resumes it instead
// of quick-exiting into a counted restart.
func argsAfterBridgeFallback(args []string, cwd string) []string {
	id := sessionIDArg(args)
	if id == "" || !bridgedSessionWritten(cwd, id) {
		return args
	}
	return agent.ResumeArgs(args, id)
}

// sessionIDArg returns the value of args' --session-id, or "".
func sessionIDArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--session-id" {
			return args[i+1]
		}
	}
	return ""
}

// briefExceedsArgv reports whether the brief at path is too large for a
// launch-time argv word. Only a bridge-capable spawn writes such a brief;
// it reaches a legacy launch only after the bridge failed.
func briefExceedsArgv(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > int64(claudeharness.ArgvPromptLimit)
}

// watchBridgeLaunch follows a bridged launch (or adopted session) until its
// mod has connected, and its opening's ack (see trackOpeningAck). If a
// launch's mod does not connect within the connect timeout while its
// opening is still unacked, nothing has run yet: the launch's generation is
// forgotten, fellBack is set and the session killed, so the supervise loop
// relaunches the legacy way with the opening on argv. An adopted session is
// live and is never killed: its mod reconnects on its own backoff and finds
// the opening still queued. Without an opening to deliver a missing bridge
// is only logged; call sites fall back to tmux on their own. ctx ends with
// the launch.
func (s *Supervisor) watchBridgeLaunch(ctx context.Context, id *procIdentity, bl bridgeLaunch, opening *openingDelivery, tmuxPath string, fellBack *atomic.Bool) {
	w := s.bridgeWiring()
	if w == nil {
		return
	}
	if bl.ticket != nil {
		go s.trackOpeningAck(ctx, id, bl, opening, tmuxPath, fellBack)
	}
	waitCtx, cancel := context.WithTimeout(ctx, w.connectTimeout)
	defer cancel()
	_, err := w.hub.WaitFor(waitCtx, bl.target.Key, func(st bridge.State) bool {
		return st.HasConnected(bl.target)
	})
	switch {
	case err == nil || ctx.Err() != nil:
	case bl.adopted:
		fmt.Fprintf(os.Stderr, "[%s] warning: the adopted session's leo bridge has not reconnected after %s; waiting for it\n", id.Name(), w.connectTimeout)
	case bl.ticket == nil || isAcked(bl.ticket):
		fmt.Fprintf(os.Stderr, "[%s] warning: leo bridge not connected after %s; messages fall back to tmux\n", id.Name(), w.connectTimeout)
	default:
		fmt.Fprintf(os.Stderr, "[%s] leo bridge not connected after %s\n", id.Name(), w.connectTimeout)
		s.fallBackFromBridge(id, bl.target, tmuxPath, fellBack, false)
	}
}

// fallBackFromBridge abandons a bridged launch: its generation is forgotten
// (dropping what it had queued) and its session killed, for the supervise
// loop to relaunch legacy-style. Unless force, a mod that connected at the
// last moment keeps the launch. A generation already over is left alone:
// its launch has ended or been abandoned already.
func (s *Supervisor) fallBackFromBridge(id *procIdentity, target bridge.Target, tmuxPath string, fellBack *atomic.Bool, force bool) {
	w := s.bridgeWiring()
	if w == nil {
		return
	}
	var forgot bool
	if force {
		forgot = w.hub.ForgetGen(target)
	} else {
		forgot = w.hub.ForgetGenUnlessConnected(target)
	}
	if !forgot {
		return
	}
	fellBack.Store(true)
	killSession(tmuxPath, id.SessionName(), id.Name())
}

// pasteOversizedOpening delivers an opening brief too large for argv to the
// legacy claude in id's session by tmux paste, recording conversation as
// having it once the paste went through.
func pasteOversizedOpening(ctx context.Context, tmuxPath string, id *procIdentity, briefPath string, opening *openingDelivery, conversation string) {
	text, err := os.ReadFile(briefPath)
	if err == nil {
		err = pasteOpeningPrompt(ctx, tmuxPath, id.SessionName(), string(text))
	}
	switch {
	case err == nil:
		opening.delivered(conversation, "")
	case ctx.Err() == nil:
		fmt.Fprintf(os.Stderr, "[%s] pasting the opening prompt: %v\n", id.Name(), err)
	}
}

// devModVersion names the materialized mod of a build with no version.
const devModVersion = "dev"

// wireBridge builds the claude mod bridge for daemon boot: the hub every
// mod connects to and the launcher that plans bridged claude launches with
// the running leo binary, wired into sv. probe nil means the real
// `claude --version`. Without a resolvable leo binary launches stay legacy.
func wireBridge(sv *Supervisor, homePath, version string, probe bridgemod.VersionProbe) (*bridge.Hub, *bridgemod.Launcher) {
	hub := bridge.New(bridge.Options{})
	if version == "" {
		version = devModVersion
	}
	leoBin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: leo bridge disabled, cannot resolve the leo binary: %v\n", err)
		sv.SetBridge(hub, nil, 0)
		return hub, nil
	}
	launcher := bridgemod.NewLauncher(bridgemod.LauncherOptions{
		StateDir:   filepath.Join(homePath, "state"),
		LeoVersion: version,
		LeoBin:     leoBin,
		LeoHome:    homePath,
		Probe:      probe,
	})
	sv.SetBridge(hub, launcher, 0)
	return hub, launcher
}

// DefaultTaskBridgeSettle is how long a persistent task waits for its
// agent's launch to settle on the bridge: the mod's connect timeout plus
// room for a launch whose mod never connected to be relaunched legacy.
const DefaultTaskBridgeSettle = bridgemod.DefaultConnectTimeout + 10*time.Second

// taskBridgePoll is how often a waiting task re-reads its agent's route.
const taskBridgePoll = 100 * time.Millisecond

// taskInjector delivers a persistent task's prompt to the agent in a tmux
// session. An agent whose leo bridge is connected gets it as the user's own
// prompt, verbatim, waiting under the invocation's ctx for the mod to accept
// it (a busy agent accepts once its turn ends; past the deadline it is
// reported queued, see taskBridgeOutcome). A bridge error — rejection, a
// lost bridge — is returned, never retried by paste: an unaccepted deliver
// may still run and would arrive twice.
//
// The ensure step may have spawned the agent a moment ago, so the injector
// first waits up to settle for its launch to settle: a bridged launch whose
// mod has connected, or a legacy one. Anything not bridged by then is
// pasted the legacy way.
func taskInjector(hub *bridge.Hub, route func(session string) BridgeRoute, settle time.Duration, paste func(ctx context.Context, session, prompt string) error) func(ctx context.Context, session, prompt string) (*harness.Result, error) {
	return func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		if hub != nil && route != nil {
			target, settled, err := awaitTaskBridge(ctx, hub, route, session, settle)
			if err != nil {
				return nil, err
			}
			if settled {
				return taskBridgeOutcome(target.Key, hub.SendTo(ctx, target, bridge.Deliver(prompt, true)))
			}
			if target.Key != "" {
				fmt.Fprintf(os.Stderr, "bridge: %s not connected; pasting the task prompt into %s\n", target.Key, session)
			}
		}
		return nil, paste(ctx, session, prompt)
	}
}

// taskBridgeOutcome turns a task deliver's send result into the injector's.
// Acked (the turn started) is fire-and-forget delivery: nil, nil, and the
// task's report closes the invocation. A deliver still unacked when the
// invocation's deadline (or the ack timeout) ran out has not failed: the
// agent is busy, the deliver stays queued and runs when its turn ends, and
// it cannot be recalled — the mod may already have handed it to claude. So
// the invocation completes now as queued, rather than as a failure, and
// rather than timing out later, which would interrupt the agent's
// unrelated running turn. Anything else (a refusal, a lost or relaunched
// bridge) is an error.
func taskBridgeOutcome(key string, err error) (*harness.Result, error) {
	switch {
	case err == nil:
		return nil, nil
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, bridge.ErrAckTimeout):
		fmt.Fprintf(os.Stderr, "bridge: %s is busy; the task prompt stays queued until its turn ends\n", key)
		return &harness.Result{Text: "queued: " + key + " was busy; the prompt runs when its current turn ends"}, nil
	default:
		return nil, err
	}
}

// awaitTaskBridge waits, up to settle, for session's launch to settle: a
// bridged launch whose mod has connected (settled; it may be between
// reconnects, which a deliver rides out queued), or a legacy one (the zero
// Target). A bridged launch whose mod has not connected by the deadline
// returns its target, unsettled. err is ctx's when the invocation itself
// ends first.
func awaitTaskBridge(ctx context.Context, hub *bridge.Hub, route func(string) BridgeRoute, session string, settle time.Duration) (target bridge.Target, settled bool, err error) {
	deadline := time.NewTimer(settle)
	defer deadline.Stop()
	// A short settle (tests) polls proportionally faster.
	poll := max(min(taskBridgePoll, settle/10), time.Millisecond)
	for {
		r := route(session)
		switch {
		case r.Target.Key != "" && hub.State(r.Target.Key).HasConnected(r.Target):
			return r.Target, true, nil
		case r.Planned && r.Target.Key == "":
			return bridge.Target{}, false, nil
		}
		select {
		case <-ctx.Done():
			return r.Target, false, ctx.Err()
		case <-deadline.C:
			return r.Target, false, nil
		case <-time.After(poll):
		}
	}
}
