package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/hosts"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/observe/httpapi"
	"github.com/blackpaw-studio/leo/internal/web"
)

func (s *Server) requireHub(w http.ResponseWriter) *hosts.Hub {
	if s.hostHub == nil {
		writeJSON(w, 503, Response{OK: false, Code: "host_unavailable", Error: "host hub unavailable"})
		return nil
	}
	return s.hostHub
}
func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	if h := s.requireHub(w); h != nil {
		writeJSON(w, 200, Response{OK: true, Data: hostRaw(h.Rows())})
	}
}
func (s *Server) handleHostConnect(w http.ResponseWriter, r *http.Request) {
	h := s.requireHub(w)
	if h == nil {
		return
	}
	row, err := h.Connect(r.Context(), r.PathValue("name"))
	writeHostResult(w, row, err)
}
func (s *Server) handleHostDisconnect(w http.ResponseWriter, r *http.Request) {
	h := s.requireHub(w)
	if h == nil {
		return
	}
	row, err := h.Disconnect(r.PathValue("name"))
	writeHostResult(w, row, err)
}
func writeHostResult(w http.ResponseWriter, row hosts.Row, err error) {
	if err == nil {
		writeJSON(w, 200, Response{OK: true, Data: hostRaw(row)})
		return
	}
	code := "host_unavailable"
	status := 503
	if he, ok := err.(*hosts.HostError); ok {
		code = he.Code
		if code == "host_unknown" {
			status = 404
		}
	}
	writeJSON(w, status, Response{OK: false, Code: code, Error: err.Error()})
}
func (s *Server) handleHostProxy(w http.ResponseWriter, r *http.Request) {
	h := s.requireHub(w)
	if h == nil {
		return
	}
	err := h.Proxy(w, r, r.PathValue("name"))
	if err != nil {
		status := 503
		if he, ok := err.(*hosts.HostError); ok && he.Code == "host_unknown" {
			status = 404
		}
		hosts.WriteError(w, status, err)
	}
}
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeJSON(w, 500, Response{OK: false, Error: err.Error()})
		return
	}
	type templateRow struct {
		Name      string `json:"name"`
		Model     string `json:"model,omitempty"`
		Agent     string `json:"agent,omitempty"`
		Workspace string `json:"workspace,omitempty"`
	}
	names := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]templateRow, 0, len(names))
	for _, name := range names {
		t := cfg.Templates[name]
		decoded, _ := claudeharness.Claude{}.DecodeOptions(t.HarnessOptions)
		opts, _ := decoded.(claudeharness.Options)
		rows = append(rows, templateRow{Name: name, Model: t.Model, Agent: opts.AgentFile, Workspace: t.Workspace})
	}
	writeJSON(w, 200, Response{OK: true, Data: hostRaw(rows)})
}
func (s *Server) handleHostEvents(w http.ResponseWriter, r *http.Request) {
	h := s.requireHub(w)
	if h == nil {
		return
	}
	localOnly := r.URL.Query().Get("scope") == "local"
	httpapi.ServeEvents(w, r, httpapi.EventsOptions{Source: s.observeBus, Covered: map[observe.EventType]bool{observe.EventHostStateChanged: true}, Heartbeat: 20 * time.Second, WriteTimeout: 30 * time.Second, Payload: func(ev observe.Event) any { return httpapi.HostPayload{Payload: ev.Payload, Host: "localhost"} }, Accept: func(ev observe.Event) bool {
		if !localOnly {
			return true
		}
		b, _ := json.Marshal(ev.Payload)
		var p struct {
			Host string `json:"host"`
		}
		_ = json.Unmarshal(b, &p)
		return p.Host == "" || p.Host == "localhost"
	}, Hello: func(_ uint64, _ time.Time) any { return map[string]any{"version": s.leoVersion} }, Initial: func() ([]httpapi.Frame, uint64) {
		seq := uint64(0)
		if s.observeBus != nil {
			seq = s.observeBus.Sequence()
		}
		rows := h.Rows()
		if localOnly {
			rows = rows[:1]
		}
		frames := make([]httpapi.Frame, 0, len(rows))
		for _, row := range rows {
			payload := map[string]any{"host": row.Name, "state": row.State}
			if row.Error != "" {
				payload["error"] = row.Error
			}
			if row.Code != "" {
				payload["code"] = row.Code
			}
			frames = append(frames, httpapi.Frame{Event: string(observe.EventHostStateChanged), Payload: payload})
		}
		return frames, seq
	}})
}
func (s *Server) handleHostState(w http.ResponseWriter, r *http.Request) {
	httpapi.ServeState(w, r, func(ctx context.Context) (any, error) {
		var records []agent.Record
		if s.agentMgr != nil {
			records = s.agentMgr.List()
		}
		var states map[string]web.ProcessStateInfo
		if s.processes != nil {
			states = (&processAdapter{inner: s.processes}).States()
		}
		cfg, _ := config.Load(s.configPath)
		projected := web.ProjectAgents(records, states, s.observeActivity, cfg)
		agents := make([]any, 0, len(projected))
		for _, a := range projected {
			b, _ := json.Marshal(a)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			m["host"] = "localhost"
			agents = append(agents, m)
		}
		if s.hostHub != nil && r.URL.Query().Get("scope") != "local" {
			agents = append(agents, s.hostHub.RemoteAgents(ctx)...)
		}
		return map[string]any{"agents": agents}, nil
	}, func(w http.ResponseWriter, status int, data any, err error) {
		if err != nil {
			writeJSON(w, status, Response{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, status, Response{OK: true, Data: hostRaw(data)})
	})
}

func hostRaw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
