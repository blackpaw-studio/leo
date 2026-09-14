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
	interactiveLeoPath, err := os.Executable()
	if err != nil {
		interactiveLeoPath, _ = filepath.Abs(s.leoPath)
	}
	runtime := consult.NewInteractiveRuntime(s.configPath, s.loadConfig, resolveCallerSession, findTmuxPath(), interactiveLeoPath)
	runtime.AgentToken = s.agentToken
	s.consults.SetInteractiveRuntime(runtime)
	s.consults.MarkInterrupted()
	if opts.ParentContext == nil {
		return
	}
	go func() {
		dispatcherTicker := time.NewTicker(5 * time.Second)
		viewerTicker := time.NewTicker(10 * time.Second)
		defer dispatcherTicker.Stop()
		defer viewerTicker.Stop()
		for {
			select {
			case <-opts.ParentContext.Done():
				return
			case now := <-dispatcherTicker.C:
				s.consults.Sweep(now)
				viewer.UpdateRoster(s.consults.Records(), now)
			case now := <-viewerTicker.C:
				viewer.Sweep(s.consults.Records(), now)
				s.consults.Prune()
			}
		}
	}()
}
