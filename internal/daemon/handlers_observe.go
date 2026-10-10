package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/observe/httpapi"
	"github.com/blackpaw-studio/leo/internal/web"
)

type versionData struct {
	Version string `json:"version"`
}

// healthData is the /health payload. Ready flips true only once the daemon
// has finished restoring agents (see Server.MarkReady); /health itself
// answers 200 from the moment the socket binds, so liveness probes that only
// read the status are unaffected by a slow restore.
type healthData struct {
	Version string `json:"version"`
	Ready   bool   `json:"ready"`
	// PID identifies this daemon boot, so a caller that restarted the
	// daemon can tell the new process from the old one still answering.
	PID int `json:"pid"`
}

// localStateData is the socket /state payload: the agent rows plus the
// dispatch rows and event-stream seq a client baselines from before applying
// /events. Meta and Dispatches match /api/v1/state.
type localStateData struct {
	Meta       observe.SnapshotMeta `json:"meta"`
	Agents     []observe.Agent      `json:"agents"`
	Dispatches []observe.Dispatch   `json:"dispatches"`
}

type templateListEntry struct {
	Name      string `json:"name"`
	Model     string `json:"model,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	// Environments is the template's default named environments (names only;
	// always an array, as in /api/v1/templates).
	Environments []string `json:"environments"`
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, versionData{Version: s.leoVersion})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.observeBus == nil {
		writeJSON(w, http.StatusServiceUnavailable, Response{
			OK: false, Error: "observability unavailable", Code: "observability_unavailable",
		})
		return
	}
	httpapi.ServeEvents(w, r, httpapi.EventsOptions{
		Source: s.observeBus, Heartbeat: 20 * time.Second, WriteTimeout: 30 * time.Second, Buffer: 32, Clock: s.observeClock,
	})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	httpapi.ServeState(w, r, func(context.Context) (any, error) {
		// Read the event seq before any state: the snapshot then reflects at
		// least every event up to it (same ordering as /api/v1/state).
		var seq uint64
		if s.observeBus != nil {
			seq = s.observeBus.Seq()
		}
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return nil, fmt.Errorf("loading config: %w", err)
		}
		var records []agent.Record
		if s.agentMgr != nil {
			records = s.agentMgr.List()
		}
		states := make(map[string]web.ProcessStateInfo)
		if s.processes != nil {
			for name, state := range s.processes.States() {
				states[name] = web.ProcessStateInfo{
					Name: state.Name, Status: state.Status, StartedAt: state.StartedAt, Restarts: state.Restarts, Ephemeral: state.Ephemeral,
				}
			}
		}
		src := s.agentSources()
		return localStateData{
			Meta:       observe.SnapshotMeta{Seq: seq},
			Agents:     web.ProjectAgents(records, states, src, cfg),
			Dispatches: web.DispatchRows(src.Dispatches, time.Now()),
		}, nil
	}, func(w http.ResponseWriter, status int, data any, err error) {
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		writeData(w, status, data)
	})
}

func (s *Server) handleTemplates(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}
	names := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]templateListEntry, 0, len(names))
	for _, name := range names {
		tmpl := cfg.Templates[name]
		envs := tmpl.Environments
		if envs == nil {
			envs = []string{}
		}
		var agentFile string
		if decoded, decodeErr := (claudeharness.Claude{}).DecodeOptions(tmpl.HarnessOptions); decodeErr == nil {
			if opts, ok := decoded.(claudeharness.Options); ok {
				agentFile = opts.AgentFile
			}
		}
		entries = append(entries, templateListEntry{Name: name, Model: tmpl.Model, Agent: agentFile, Workspace: tmpl.Workspace, Environments: envs})
	}
	writeData(w, http.StatusOK, entries)
}

// handleEnvironments lists the configured named environments, names only, via
// GET /environments: the socket twin of GET /api/v1/environments.
func (s *Server) handleEnvironments(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		// The loader's error can quote the rejected file's contents, so the
		// detail stays in the log, exactly as GET /api/v1/environments does.
		log.Printf("daemon: loading config for GET /environments: %v", err)
		writeJSON(w, http.StatusInternalServerError, Response{Error: web.ConfigUnavailableMessage, Code: web.CodeConfigUnavailable})
		return
	}
	writeData(w, http.StatusOK, web.ListEnvironments(cfg))
}

func writeData(w http.ResponseWriter, status int, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, status, Response{OK: true, Data: b})
}

// setWebSources records the web server's bridge feed and dispatch counts
// for the local /state rows.
func (s *Server) setWebSources(src web.AgentSources) {
	s.webSources.Store(&src)
}

// agentSources are the per-agent sources the local /state rows merge: the
// daemon's own observability stores plus, once StartWeb has built it, the
// web server's bridge feed and dispatch counts.
func (s *Server) agentSources() web.AgentSources {
	src := web.AgentSources{Activity: s.observeActivity, Attention: s.observeAttention, Surfaced: s.observeSurfaced}
	if w := s.webSources.Load(); w != nil {
		src.BridgeFeed, src.Dispatches = w.BridgeFeed, w.Dispatches
	}
	return src
}
