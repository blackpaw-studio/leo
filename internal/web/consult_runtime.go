package web

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// consultLoopIntervals are the consult runtime loop's tick periods. rosterIdle
// is how often the roster refreshes when no record is roster-eligible.
type consultLoopIntervals struct {
	sweep, roster, rosterIdle, viewer time.Duration
	// bridgeState paces the state pushed to bridged claudes' mods: at most
	// one snapshot per key per tick.
	bridgeState time.Duration
}

var defaultConsultLoopIntervals = consultLoopIntervals{
	sweep:       5 * time.Second,
	roster:      time.Second,
	rosterIdle:  5 * time.Second,
	viewer:      10 * time.Second,
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
		bridgeState: pick(i.bridgeState, d.bridgeState),
	}
}

// setupConsultRuntime keeps dispatch viewer command execution on the server's
// injectable context-aware seam; this is the path used by roster sweeps too.
func (s *Server) setupConsultRuntime(opts Options, resolveCallerSession func(string) (string, bool)) {
	viewer := consult.NewViewer(s.configPath, resolveCallerSession)
	viewer.TmuxPath = findTmuxPath()
	viewer.ExecCommand = s.execCommand
	viewer.ExecCommandContext = s.execCommandContext
	s.consults = consult.NewDispatcherWithOnStart(opts.ConsultRecorder, opts.ParentContext, viewer.OnStart, viewer.Close)
	s.consults.LeoMCP = opts.LeoMCP
	viewer.Coordinator = s.consults.PlacementCoordinator()
	viewer.Records = s.consults.Records
	viewer.PersistRecord = s.consults.PersistViewerRecord
	viewer.PersistIntent = s.consults.PersistViewerRecord
	viewer.ClosePane = s.consults.CloseRecordedPane
	s.consults.SetCloseFinishedViewer(viewer.CloseFinished)
	s.consults.SetNotificationDelivery(s.notificationDelivery(opts.Bridge.Router))
	interactiveLeoPath, err := os.Executable()
	if err != nil {
		interactiveLeoPath, _ = filepath.Abs(s.leoPath)
	}
	runtime := consult.NewInteractiveRuntime(s.configPath, s.loadConfig, resolveCallerSession, findTmuxPath(), interactiveLeoPath)
	runtime.ExecCommandContext = s.execCommandContext
	runtime.AgentToken = s.agentToken
	runtime.LeoMCP = opts.LeoMCP
	s.bridgeRouter = opts.Bridge.Router
	var pusher *consult.StatePusher
	if hub := opts.Bridge.hub(); hub != nil && opts.Bridge.Launcher != nil {
		runtime.SetBridge(consult.InteractiveBridge{Hub: hub, Launcher: opts.Bridge.Launcher})
		hub.AddSubscriber(runtime.DispatchBridgeSubscriber(s.consults))
		s.dispatchBridge = runtime
		hub.SetRequestHandler(s.consults.BridgeRequestHandler())
		delegation := &delegationSource{path: s.configPath, load: s.loadConfig}
		pusher = &consult.StatePusher{Hub: hub, Records: s.consults.RunRecords, Delegation: delegation.Get, Now: time.Now}
	}
	if hub := opts.Bridge.hub(); hub != nil && s.bridgeFeed == nil {
		feed := s.newBridgeFeed(opts.Bridge.Router)
		hub.AddSubscriber(feed)
		s.bridgeFeed = feed
	}
	// Outstanding dispatch counts hold a finished turn at working (B-051):
	// read when a turn finishes (so a just-started dispatch counts) and
	// reconciled every state tick (so one that ended between ticks is
	// released). Counting by the caller's bridge key follows a rename.
	dispatchObserver := consult.NewDispatchObserver(s.consults.RunRecords, s.publisher,
		consult.WithDispatchOwner(s.dispatchCallerOwner))
	s.attention.SetDispatchCounter(dispatchObserver)
	if s.dispatches == nil {
		s.dispatches = dispatchObserver
	}
	s.consults.SetInteractiveRuntime(runtime)
	s.consults.MarkInterrupted()
	// Best-effort: drop settings spill files (which can hold credentials)
	// of runs the previous daemon never finalized.
	s.consults.SweepRunFiles()
	if opts.ParentContext == nil {
		return
	}
	intervals := s.consultIntervals.withDefaults()
	updateRoster := s.updateRoster
	if updateRoster == nil {
		updateRoster = viewer.UpdateRoster
	}
	loopCtx, cancel := context.WithCancel(opts.ParentContext)
	done := make(chan struct{})
	s.stopConsultLoop = func() {
		cancel()
		<-done
	}
	go func() {
		defer close(done)
		dispatcherTicker := time.NewTicker(intervals.sweep)
		rosterTicker := time.NewTicker(intervals.roster)
		viewerTicker := time.NewTicker(intervals.viewer)
		stateTicker := time.NewTicker(intervals.bridgeState)
		lastRosterUpdate := time.Now()
		defer dispatcherTicker.Stop()
		defer rosterTicker.Stop()
		defer viewerTicker.Stop()
		defer stateTicker.Stop()
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
			case now := <-viewerTicker.C:
				viewer.Sweep(s.consults.Records(), now)
				s.consults.Prune()
			case <-stateTicker.C:
				if pusher != nil {
					pusher.Tick()
				}
				dispatchObserver.Tick()
				s.attention.ReconcileDispatches(dispatchObserver.OutstandingDispatches())
			}
		}
	}()
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

// dispatchCallerOwner returns the agent holding a dispatch caller's bridge
// key now; a calling dispatch's key belongs to no agent.
func (s *Server) dispatchCallerOwner(key string) (string, bool) {
	if _, isDispatch := consult.DispatchIDFromBridgeKey(key); isDispatch {
		return "", false
	}
	return s.bridgeKeyOwner(key)
}
