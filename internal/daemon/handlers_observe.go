package daemon

import (
	"context"
	"encoding/json"
	"fmt"
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

type localStateData struct {
	Agents []observe.Agent `json:"agents"`
}

type templateListEntry struct {
	Name      string `json:"name"`
	Model     string `json:"model,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Workspace string `json:"workspace,omitempty"`
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
		return localStateData{Agents: web.ProjectAgents(records, states, s.observeActivity, s.observeAttention, s.observeSurfaced, cfg)}, nil
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
		var agentFile string
		if decoded, decodeErr := (claudeharness.Claude{}).DecodeOptions(tmpl.HarnessOptions); decodeErr == nil {
			if opts, ok := decoded.(claudeharness.Options); ok {
				agentFile = opts.AgentFile
			}
		}
		entries = append(entries, templateListEntry{Name: name, Model: tmpl.Model, Agent: agentFile, Workspace: tmpl.Workspace})
	}
	writeData(w, http.StatusOK, entries)
}

func writeData(w http.ResponseWriter, status int, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, status, Response{OK: true, Data: b})
}
