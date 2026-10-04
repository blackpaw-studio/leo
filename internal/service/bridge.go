package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// a bridged launch sets them, every other launch blanks them.
var bridgeEnvKeys = []string{bridgemod.EnvBin, bridgemod.EnvAgent}

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

// BridgeKeyForSession returns the bridge key of the agent running in tmux
// session, if its launch loaded the bridge.
func (s *Supervisor) BridgeKeyForSession(session string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.identities {
		if id.SessionName() == session {
			key := id.BridgeKey()
			return key, key != ""
		}
	}
	return "", false
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
// any other live claude agent (launched legacy, or its mod gone), and ""
// for other harnesses and unknown names.
func (s *Supervisor) BridgeStatus(name string) string {
	s.mu.RLock()
	id, ok := s.identities[name]
	s.mu.RUnlock()
	if !ok || (id.harness != "" && id.harness != "claude") {
		return ""
	}
	if _, live := s.BridgeState(name); live {
		return agent.BridgeConnected
	}
	return agent.BridgeAbsent
}

// BridgeRouter routes agent names to their live bridges; nil without a hub.
func (s *Supervisor) BridgeRouter() *bridge.Router {
	w := s.bridgeWiring()
	if w == nil {
		return nil
	}
	return &bridge.Router{Hub: w.hub, Keys: s.BridgeKey}
}

// BridgeCapable reports whether a claude agent launched now would load the
// bridge, so its opening prompt need not fit on argv.
func (s *Supervisor) BridgeCapable() bool {
	w := s.bridgeWiring()
	return w != nil && w.launcher != nil && w.launcher.Capable(s.ctx, s.claudePath)
}

// allocBridgeKey picks the key name's launch connects under: the name
// itself, unless another live identity's claude (renamed away since its
// launch) still holds it, then name.<random>. Dots never appear in agent
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

func (s *Supervisor) bridgeKeyTakenLocked(key string, self *procIdentity) bool {
	for _, other := range s.identities {
		if other != self && other.BridgeKey() == key {
			return true
		}
	}
	return false
}

// forgetBridge drops id's bridge state in the hub, if it had any.
func (s *Supervisor) forgetBridge(id *procIdentity) {
	w := s.bridgeWiring()
	if w == nil || id == nil {
		return
	}
	if key := id.BridgeKey(); key != "" {
		w.hub.Forget(key)
	}
}

// tmuxSessionBridgeKey reads the bridge key a running session was launched
// with from its environment; ok is false when unset or unusable.
func tmuxSessionBridgeKey(tmuxPath, session string) (string, bool) {
	out, err := exec.Command(tmuxPath, tmux.Args("show-environment", "-t", tmux.Target(session), bridgemod.EnvAgent)...).Output() // #nosec G204 -- fixed tmux argv
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(out))
	key, found := strings.CutPrefix(line, bridgemod.EnvAgent+"=")
	if !found || !config.ValidName(key) {
		return "", false
	}
	return key, true
}

// bridgeLaunch is how one claude launch uses the bridge. The zero value is a
// legacy launch.
type bridgeLaunch struct {
	plan    bridgemod.Plan
	bridged bool
	// opening is the opening prompt the bridge delivers as this launch's
	// first user prompt ("" for none); the launch then omits it from argv.
	opening string
}

// planBridgeLaunch decides how this launch of id uses the bridge and, when
// withOpening, reads the opening prompt for the bridge to deliver. It
// records the launch's bridge key on id ("" for a legacy launch).
func (s *Supervisor) planBridgeLaunch(ctx context.Context, claudePath, harnessName string, id *procIdentity, spec ProcessSpec, withOpening, forceLegacy bool) bridgeLaunch {
	w := s.bridgeWiring()
	if w == nil || w.launcher == nil || forceLegacy || harnessName != "claude" {
		id.setBridgeKey("")
		return bridgeLaunch{}
	}
	plan, ok := w.launcher.Plan(ctx, claudePath, s.allocBridgeKey(id.Name(), id))
	if !ok {
		id.setBridgeKey("")
		return bridgeLaunch{}
	}
	id.setBridgeKey(plan.Key)
	launch := bridgeLaunch{plan: plan, bridged: true}
	if !withOpening || spec.OpeningBriefPath == "" {
		return launch
	}
	text, err := os.ReadFile(spec.OpeningBriefPath)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "[%s] warning: reading the opening brief for the bridge: %v; it rides argv\n", id.Name(), err)
	case len(text) == 0:
		// Nothing to deliver; the argv path handles an empty brief as it
		// always has.
	default:
		launch.opening = string(text)
	}
	return launch
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

// watchBridgeLaunch queues a bridged launch's opening prompt and waits for
// its mod to connect. If it does not connect within the connect timeout and
// the launch has an opening prompt, nothing has run yet: the bridge is
// forgotten, fellBack is set and the session killed, so the supervise loop
// relaunches the legacy way. Without an opening prompt a missing bridge is
// only logged; call sites fall back to tmux on their own. ctx ends with the
// launch.
func (s *Supervisor) watchBridgeLaunch(ctx context.Context, id *procIdentity, bl bridgeLaunch, tmuxPath string, fellBack *atomic.Bool) {
	w := s.bridgeWiring()
	if w == nil {
		return
	}
	key := bl.plan.Key
	if bl.opening != "" {
		if _, err := w.hub.Enqueue(key, bridge.Deliver(bl.opening, true)); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] queueing the opening prompt on the bridge: %v\n", id.Name(), err)
			s.fallBackFromBridge(id, key, tmuxPath, fellBack)
			return
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, w.connectTimeout)
	defer cancel()
	_, err := w.hub.WaitFor(waitCtx, key, func(st bridge.State) bool { return st.Connected })
	if err == nil || ctx.Err() != nil {
		return
	}
	if bl.opening == "" {
		fmt.Fprintf(os.Stderr, "[%s] warning: leo bridge not connected after %s; messages fall back to tmux\n", id.Name(), w.connectTimeout)
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] leo bridge not connected after %s\n", id.Name(), w.connectTimeout)
	s.fallBackFromBridge(id, key, tmuxPath, fellBack)
}

// fallBackFromBridge abandons a bridged launch whose mod is not connected:
// its queued commands are dropped, and its session is killed for the
// supervise loop to relaunch legacy-style. A bridge that connected at the
// last moment is kept.
func (s *Supervisor) fallBackFromBridge(id *procIdentity, key, tmuxPath string, fellBack *atomic.Bool) {
	w := s.bridgeWiring()
	if w == nil || !w.hub.ForgetUnlessConnected(key) {
		return
	}
	fellBack.Store(true)
	killSession(tmuxPath, id.SessionName(), id.Name())
}

// pasteOversizedOpening delivers an opening brief too large for argv to the
// legacy claude in id's session by tmux paste.
func pasteOversizedOpening(ctx context.Context, tmuxPath string, id *procIdentity, briefPath string) {
	text, err := os.ReadFile(briefPath)
	if err == nil {
		err = pasteOpeningPrompt(ctx, tmuxPath, id.SessionName(), string(text))
	}
	if err != nil && ctx.Err() == nil {
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
		Probe:      probe,
	})
	sv.SetBridge(hub, launcher, 0)
	return hub, launcher
}

// taskInjector delivers a persistent task's prompt to the agent in a tmux
// session. An agent whose leo bridge is connected gets it as the user's own
// prompt, verbatim, waiting under the invocation's ctx for the mod to accept
// it (a busy agent accepts once its turn ends). A bridge error — rejection,
// a lost bridge, the deadline — is returned, never retried by paste: an
// unaccepted deliver stays queued and would arrive twice. Anything else is
// pasted the legacy way.
func taskInjector(hub *bridge.Hub, route func(session string) (string, bool), paste func(ctx context.Context, session, prompt string) error) func(ctx context.Context, session, prompt string) (*harness.Result, error) {
	return func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		if hub != nil && route != nil {
			if key, ok := route(session); ok {
				if hub.Connected(key) {
					return nil, hub.Send(ctx, key, bridge.Deliver(prompt, true))
				}
				fmt.Fprintf(os.Stderr, "bridge: %s not connected; pasting the task prompt into %s\n", key, session)
			}
		}
		return nil, paste(ctx, session, prompt)
	}
}
