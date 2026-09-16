package web

import (
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// setupConsultRuntime keeps dispatch viewer command execution on the server's
// injectable context-aware seam; this is the path used by roster sweeps too.
func (s *Server) setupConsultRuntime(opts Options, resolveCallerSession func(string) (string, bool)) {
	viewer := consult.NewViewer(s.configPath, resolveCallerSession)
	viewer.TmuxPath = findTmuxPath()
	viewer.ExecCommand = s.execCommand
	viewer.ExecCommandContext = s.execCommandContext
	s.consults = consult.NewDispatcherWithOnStart(opts.ConsultRecorder, opts.ParentContext, viewer.OnStart, viewer.Close)
	viewer.Coordinator = s.consults.PlacementCoordinator()
	viewer.Records = s.consults.Records
	viewer.PersistRecord = s.consults.PersistViewerRecord
	viewer.PersistIntent = s.consults.PersistViewerRecord
	viewer.ClosePane = s.consults.CloseRecordedPane
	s.consults.SetCloseFinishedViewer(viewer.CloseFinished)
	s.consults.SetNotificationDelivery(consult.NewTmuxNotificationDelivery(findTmuxPath(), s.execCommandContext))
	interactiveLeoPath, err := os.Executable()
	if err != nil {
		interactiveLeoPath, _ = filepath.Abs(s.leoPath)
	}
	runtime := consult.NewInteractiveRuntime(s.configPath, s.loadConfig, resolveCallerSession, findTmuxPath(), interactiveLeoPath)
	runtime.ExecCommandContext = s.execCommandContext
	runtime.AgentToken = s.agentToken
	s.consults.SetInteractiveRuntime(runtime)
	s.consults.MarkInterrupted()
	if opts.ParentContext == nil {
		return
	}
	go func() {
		dispatcherTicker := time.NewTicker(5 * time.Second)
		rosterTicker := time.NewTicker(time.Second)
		viewerTicker := time.NewTicker(10 * time.Second)
		lastRosterUpdate := time.Now()
		defer dispatcherTicker.Stop()
		defer rosterTicker.Stop()
		defer viewerTicker.Stop()
		for {
			select {
			case <-opts.ParentContext.Done():
				return
			case now := <-dispatcherTicker.C:
				s.consults.Sweep(now)
			case now := <-rosterTicker.C:
				records := s.consults.Records()
				if consult.HasRosterEligibleRecord(records, now) || now.Sub(lastRosterUpdate) >= 5*time.Second {
					viewer.UpdateRoster(records, now)
					lastRosterUpdate = now
				}
			case now := <-viewerTicker.C:
				viewer.Sweep(s.consults.Records(), now)
				s.consults.Prune()
			}
		}
	}()
}
