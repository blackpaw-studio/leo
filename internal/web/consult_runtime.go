package web

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/startup"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// restartReconcileWorkers is how many records the background restart
// reconcile works on at once.
const restartReconcileWorkers = 4

// StartRestartReconcile starts the background pass that finishes the
// restart reconciliation MarkInterrupted queued (see
// consult.Dispatcher.ReconcileRestart). The daemon calls it once it is
// ready; later calls do nothing.
func (s *Server) StartRestartReconcile() {
	if s.startRestartReconcile != nil {
		s.startRestartReconcile()
	}
}

// consultLoopIntervals are the consult runtime loop's tick periods. rosterIdle
// is how often the roster refreshes when no record is roster-eligible.
type consultLoopIntervals struct {
	sweep, roster, rosterIdle, viewer time.Duration
	// placement paces the live placement watch (see Dispatcher.PollPlacement):
	// one tmux list-clients per tick while a dispatch viewer is live.
	placement time.Duration
	// bridgeState paces the state pushed to bridged claudes' mods: at most
	// one snapshot per key per tick.
	bridgeState time.Duration
}

var defaultConsultLoopIntervals = consultLoopIntervals{
	sweep:       5 * time.Second,
	roster:      time.Second,
	rosterIdle:  5 * time.Second,
	viewer:      10 * time.Second,
	placement:   time.Second,
	bridgeState: time.Second,
}

// withDefaults fills zero fields from defaultConsultLoopIntervals.
func (i consultLoopIntervals) withDefaults() consultLoopIntervals {
	pick := func(v, def time.Duration) time.Duration {
		if v > 0 {
			return v
		}
		return def
	}
	d := defaultConsultLoopIntervals
	return consultLoopIntervals{
		sweep:       pick(i.sweep, d.sweep),
		roster:      pick(i.roster, d.roster),
		rosterIdle:  pick(i.rosterIdle, d.rosterIdle),
		viewer:      pick(i.viewer, d.viewer),
		placement:   pick(i.placement, d.placement),
		bridgeState: pick(i.bridgeState, d.bridgeState),
	}
}

// setupConsultRuntime keeps dispatch viewer command execution on the server's
// injectable context-aware seam; this is the path used by roster sweeps too.
func (s *Server) setupConsultRuntime(opts Options, resolveCallerSession func(string) (string, bool)) {
	// One counter spans every seam the restart path shells out through, so
	// the startup log can say how many tmux and git calls MarkInterrupted made.
	var calls startup.ExecCounter
	s.execCommand = calls.Wrap(s.execCommand)
	s.execCommandContext = calls.WrapContext(s.execCommandContext)
	viewer := consult.NewViewer(s.configPath, resolveCallerSession)
	viewer.TmuxPath = s.tmuxPath()
	viewer.ExecCommand = s.execCommand
	viewer.ExecCommandContext = s.execCommandContext
	s.consults = consult.NewDispatcherWithOnStart(opts.ConsultRecorder, opts.ParentContext, viewer.OnStart, viewer.Close)
	s.consults.LeoMCP = opts.LeoMCP
	s.consults.GitCommand = calls.Wrap(s.consults.GitCommand)
	s.consults.SetStartupLog(opts.Startup.Logf)
	if cfg, err := s.loadConfig(); err == nil {
		s.consults.ApplyConfig(cfg)
	}
	s.consults.SetAttachPlacements(opts.AttachPlacements, func(ctx context.Context, session string) ([]tmux.Client, error) {
		return tmux.ListClients(ctx, s.tmuxPath(), session)
	})
	s.consults.SetAllClientsLister(func(ctx context.Context) ([]tmux.SessionClient, error) {
		return tmux.ListAllClients(ctx, s.tmuxPath())
	})
	viewer.PlacementOverrides = s.consults.ApplyAttachPlacement
	viewer.Coordinator = s.consults.PlacementCoordinator()
	viewer.Records = s.consults.Records
	viewer.PersistRecord = s.consults.PersistViewerRecord
	viewer.PersistIntent = s.consults.PersistViewerRecord
	viewer.ClosePane = s.consults.CloseRecordedPane
	s.consults.SetCloseFinishedViewer(viewer.CloseFinished)
	s.consults.SetNotificationDelivery(s.notificationDelivery(opts.Bridge.Router))
	runtime := consult.NewInteractiveRuntime(s.configPath, s.loadConfig, resolveCallerSession, s.tmuxPath(), interactiveLeoPath(opts.LeoMCP.Bin, os.Executable, s.leoPath))
	runtime.ExecCommandContext = s.execCommandContext
	runtime.AgentTokenFile = func(cfg *config.Config) string { return AgentTokenPath(cfg.StatePath()) }
	s.consults.AgentToken = s.agentToken
	runtime.LeoMCP = opts.LeoMCP
	s.bridgeRouter = opts.Bridge.Router
	var pusher *consult.StatePusher
	cutoffs := consult.NewRosterCutoffs(time.Now)
	viewer.Cutoffs = cutoffs
	if hub := opts.Bridge.hub(); hub != nil && opts.Bridge.Launcher != nil {
		runtime.SetBridge(consult.InteractiveBridge{Hub: hub, Launcher: opts.Bridge.Launcher})
		hub.AddSubscriber(runtime.DispatchBridgeSubscriber(s.consults))
		s.dispatchBridge = runtime
		hub.SetRequestHandler(s.consults.BridgeRequestHandler())
		delegation := &delegationSource{path: s.configPath, load: s.loadConfig}
		s.delegationCtx = s.newDelegationContext(delegation.Get)
		pusher = &consult.StatePusher{Hub: hub, Records: s.consults.RunRecords, Delegation: delegation.Get, Now: time.Now, Cutoffs: cutoffs}
		hub.AddSubscriber(cutoffs)
		// The tmux roster leaves out what a bridged caller's band shows, so
		// only while the pusher feeds those bands state.
		viewer.BridgeConnected = hub.Connected
	}
	if hub := opts.Bridge.hub(); hub != nil && s.bridgeFeed == nil {
		feed := s.newBridgeFeed(opts.Bridge.Router)
		hub.AddSubscriber(feed)
		s.bridgeFeed = feed
	}
	// Outstanding dispatch counts hold a finished turn at working (B-051):
	// read when a turn finishes (so a just-started dispatch counts) and
	// reconciled every state tick (so one that ended between ticks is
	// released). Only dispatches by a bridged caller count, by its key, so
	// the count follows a rename.
	dispatchObserver := consult.NewDispatchObserver(s.consults.RunRecords, s.publisher,
		consult.WithDispatchOwner(s.dispatchCallerOwner))
	s.attention.SetDispatchCounter(dispatchObserver)
	if s.dispatches == nil {
		s.dispatches = dispatchObserver
	}
	s.consults.SetInteractiveRuntime(runtime)
	// Adopting surviving dispatches re-opens their bridge keys, so it must
	// run before the agents' restore settles adoption (Hub.AwaitAdoption):
	// after that, a key nobody opened refuses its mod for good.
	// This is the fast pass: records already terminal, worktree cleanup and
	// the settings-spill sweep are left to the background restart reconcile
	// the daemon starts once it is ready (StartRestartReconcile).
	stop := opts.Startup.Step("web.New: MarkInterrupted")
	before := calls.Snapshot()
	restart := s.consults.MarkInterrupted()
	used := calls.Snapshot().Sub(before)
	stop()
	opts.Startup.Logf("startup: MarkInterrupted: %d records, %d tmux calls, %d git calls (%d timed out), %d queued for the restart reconcile",
		restart.Records, used.Tmux, used.Git, restart.GitTimeouts, restart.Queued)
	if opts.ParentContext == nil {
		return
	}
	intervals := s.consultIntervals.withDefaults()
	updateRoster := s.updateRoster
	if updateRoster == nil {
		updateRoster = viewer.UpdateRoster
	}
	pollPlacement := s.pollPlacement
	if pollPlacement == nil {
		pollPlacement = s.consults.PollPlacement
	}
	prune := s.pruneConsults
	if prune == nil {
		prune = s.consults.Prune
	}
	loopCtx, cancel := context.WithCancel(opts.ParentContext)
	done := make(chan struct{})
	placementDone := make(chan struct{})
	// reconciled closes once the restart reconcile has exited, or once
	// shutdown has made sure it never starts.
	reconciled := make(chan struct{})
	var reconcileOnce sync.Once
	s.startRestartReconcile = func() {
		reconcileOnce.Do(func() {
			go func() {
				defer close(reconciled)
				before := calls.Snapshot()
				summary := s.consults.ReconcileRestart(loopCtx, restartReconcileWorkers)
				opts.Startup.Logf("startup: %s", summary.LogLine(calls.Snapshot().Sub(before).Tmux))
			}()
		})
	}
	s.stopConsultLoop = func() {
		cancel()
		reconcileOnce.Do(func() { close(reconciled) })
		<-done
		<-placementDone
		<-reconciled
	}
	// A placement poll waits on tmux moves, so it gets a loop of its own
	// rather than stalling the sweep and the roster.
	go func() {
		defer close(placementDone)
		ticks := s.placementTicks
		if ticks == nil {
			ticker := time.NewTicker(intervals.placement)
			defer ticker.Stop()
			ticks = ticker.C
		}
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticks:
				pollPlacement(loopCtx)
			}
		}
	}()
	go func() {
		defer close(done)
		dispatcherTicker := time.NewTicker(intervals.sweep)
		rosterTicker := time.NewTicker(intervals.roster)
		stateTicker := time.NewTicker(intervals.bridgeState)
		lastRosterUpdate := time.Now()
		defer dispatcherTicker.Stop()
		defer rosterTicker.Stop()
		defer stateTicker.Stop()
		viewerTicks := s.viewerTicks
		if viewerTicks == nil {
			viewerTicker := time.NewTicker(intervals.viewer)
			defer viewerTicker.Stop()
			viewerTicks = viewerTicker.C
		}
		for {
			select {
			case <-loopCtx.Done():
				return
			case now := <-dispatcherTicker.C:
				s.consults.Sweep(now)
			case now := <-rosterTicker.C:
				records := s.consults.Records()
				if consult.HasRosterEligibleRecord(records, now) || now.Sub(lastRosterUpdate) >= intervals.rosterIdle {
					updateRoster(records, now)
					lastRosterUpdate = now
				}
			case now := <-viewerTicks:
				// Records the restart reconcile has yet to reach are left to
				// it: a viewer write or a prune meanwhile would be lost to,
				// or undone by, its own write.
				viewer.Sweep(withoutIDs(s.consults.Records(), s.consults.RestartPendingIDs()), now)
				select {
				case <-reconciled:
					prune()
				default:
				}
			case <-stateTicker.C:
				if pusher != nil {
					pusher.Tick()
				}
				dispatchObserver.Tick()
				s.attention.ReconcileDispatches(dispatchObserver.DispatchSnapshot())
			}
		}
	}()
}

// withoutIDs returns records minus those whose id is in ids.
func withoutIDs(records []consult.Record, ids map[string]bool) []consult.Record {
	if len(ids) == 0 {
		return records
	}
	kept := make([]consult.Record, 0, len(records))
	for _, rec := range records {
		if !ids[rec.ID] {
			kept = append(kept, rec)
		}
	}
	return kept
}

// newBridgeFeed builds the observability projection of the agents' bridge
// events, resolving each bridge key to the agent router routes to it.
func (s *Server) newBridgeFeed(router *bridge.Router) *observe.BridgeFeed {
	return observe.NewBridgeFeed(observe.KeyResolverFunc(s.agentForBridgeKey(router)), s.publisher,
		observe.WithFeedAttention(s.attention),
		observe.WithFeedActivity(s.activity),
		observe.WithFeedConnected(router.Connected))
}

// agentForBridgeKey returns the reverse of router.Key over the supervised
// agents. Dispatch keys are never agents.
func (s *Server) agentForBridgeKey(router *bridge.Router) func(key string) (string, bool) {
	return func(key string) (string, bool) {
		if _, isDispatch := consult.DispatchIDFromBridgeKey(key); isDispatch || key == "" {
			return "", false
		}
		// Most agents' key is their own name: check that before scanning.
		if k, ok := router.Key(key); ok && k == key {
			return key, true
		}
		if s.agentSvc == nil {
			return "", false
		}
		for _, rec := range s.agentSvc.List() {
			if k, ok := router.Key(rec.Name); ok && k == key {
				return rec.Name, true
			}
		}
		return "", false
	}
}

// dispatchCallerOwner returns the agent whose live launch a dispatch
// caller's bridge key and launch id name: none once that launch is gone,
// even if a later agent took the key. A calling dispatch's key belongs to
// no agent.
func (s *Server) dispatchCallerOwner(key, launch string) (string, bool) {
	if _, isDispatch := consult.DispatchIDFromBridgeKey(key); isDispatch {
		return "", false
	}
	if current := s.bridgeLaunchID(key); current == "" || current != launch {
		return "", false
	}
	return s.bridgeKeyOwner(key)
}

// bridgeLaunchID returns the launch id (bridge.LaunchID) of the launch
// bridge key key is open for now; "" for none. A dispatch caller asking
// under the key is that launch: its claude is the one connected under it.
func (s *Server) bridgeLaunchID(key string) string {
	if key == "" || s.bridgeRouter == nil || s.bridgeRouter.Hub == nil {
		return ""
	}
	id, _ := s.bridgeRouter.Hub.LaunchID(key)
	return id
}

// interactiveLeoPath is the leo binary interactive dispatch hooks call back
// into. daemonBin is the daemon's resolved binary (a managed install's stable
// self_path); without it the running executable is used, and when that fails
// too the absolute form of fallback.
func interactiveLeoPath(daemonBin string, executable func() (string, error), fallback string) string {
	if daemonBin != "" {
		return daemonBin
	}
	if path, err := executable(); err == nil {
		return path
	}
	abs, _ := filepath.Abs(fallback)
	return abs
}
