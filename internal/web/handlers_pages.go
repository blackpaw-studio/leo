package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// pageData is the payload every full-page render receives. Pages add their
// own data via the Data field. Status carries what partials/status.html
// renders today — the same statusData value backs both the full-page shell
// and the standalone /partials/status poll target (handlePartialStatus), so
// the fragment never drifts from what a full page render would show.
type pageData struct {
	Page          string
	Title         string
	Status        statusData
	RestartNeeded bool
	Data          any
}

// statusData is the subset of dashboard state partials/status.html renders:
// process/task counts and the next scheduled task run. It intentionally
// excludes per-process detail (that lives on the /processes page) and
// RestartNeeded (which stays a top-level pageData field so status.html's
// existing `.RestartNeeded` reference keeps working unmodified).
type statusData struct {
	ProcessCount int
	TaskCount    int
	NextRunName  string
	NextRunTime  time.Time
}

// nextScheduledRun returns the name and time of the earliest upcoming cron
// run across all scheduled entries. It returns ("", zero time) if there is
// no scheduler or no entries. Shared by fillStatus and buildDashboardData so
// the "earliest next run" logic exists in exactly one place.
func (s *Server) nextScheduledRun() (name string, at time.Time) {
	if s.scheduler == nil {
		return "", time.Time{}
	}
	for _, e := range s.scheduler.List() {
		if at.IsZero() || e.Next.Before(at) {
			at = e.Next
			name = e.Name
		}
	}
	return name, at
}

// fillStatus populates pd.Status and pd.RestartNeeded. It's the refactor of
// the status portion of buildDashboardData: process/task counts and the
// earliest next cron run, without the heavier per-process/per-task detail
// buildDashboardData also loads for pages that need it.
func (s *Server) fillStatus(pd *pageData) error {
	cfg, err := s.loadConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	nextRunName, nextRunTime := s.nextScheduledRun()

	pd.Status = statusData{
		ProcessCount: len(cfg.Processes),
		TaskCount:    len(cfg.Tasks),
		NextRunName:  nextRunName,
		NextRunTime:  nextRunTime,
	}
	pd.RestartNeeded = s.restartNeeded.Load()
	return nil
}

// handlePartialStatus renders partials/status.html for the 5s poll target.
// It builds the same pageData shape handlePage uses so the fragment matches
// what a full page render would show.
func (s *Server) handlePartialStatus(w http.ResponseWriter, r *http.Request) {
	var pd pageData
	if err := s.fillStatus(&pd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "status.html", pd) //nolint:errcheck
}

// handlePage returns a GET handler that renders the named page in the shell.
// build may be nil for pages with no extra data yet (sessions, service).
func (s *Server) handlePage(page, title string, build func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var data any
		if build != nil {
			var err error
			if data, err = build(r); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		pd := pageData{Page: page, Title: title, Data: data}
		if err := s.fillStatus(&pd); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.templates.ExecuteTemplate(w, "layout.html", pd); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// buildTasksData, buildProcessesData, and buildTemplatesData all reuse
// buildDashboardData: the underlying partials they transclude (tasks.html,
// config_tasks.html, processes.html, config_processes.html,
// config_templates.html) already expect that struct's shape (.Tasks,
// .Processes, .Config, .Agents), so this keeps the page cutover a pure
// wiring change rather than a data-model rewrite.

func (s *Server) buildTasksData(r *http.Request) (any, error) {
	return s.buildDashboardData()
}

func (s *Server) buildProcessesData(r *http.Request) (any, error) {
	return s.buildDashboardData()
}

func (s *Server) buildTemplatesData(r *http.Request) (any, error) {
	return s.buildDashboardData()
}

// buildDefaultsData feeds page_config_defaults with a schema-driven form
// over cfg.Defaults. Defaults is the top of the inheritance chain, so it
// gets no "inherit" placeholders of its own (see buildForm).
func (s *Server) buildDefaultsData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return s.buildForm(schema.SectionDefaults, &cfg.Defaults, cfg, "/web/config/defaults"), nil
}

// buildSettingsData feeds page_config_settings, which only needs .Web.*
// from the loaded config.
func (s *Server) buildSettingsData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return cfg, nil
}

// buildProvidersData feeds page_config_providers, which lists .Providers
// from the loaded config. Read-only in this task — provider CRUD has no
// backend mutation routes yet.
func (s *Server) buildProvidersData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return cfg, nil
}

// agentsData feeds page_agents and the /partials/agents rename fragment.
type agentsData struct {
	Agents    []agentData
	Templates any
}

// buildAgentsData loads the spawn-form templates and running ephemeral
// agents. Shared by the /agents page and handlePartialAgents (used directly
// by handleWebAgentRename to re-render the list after a rename).
func (s *Server) buildAgentsData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	var agents []agentData
	if s.agentSvc != nil {
		for _, a := range s.agentSvc.List() {
			agents = append(agents, agentData{
				Name:      a.Name,
				Status:    a.Status,
				StartedAt: a.StartedAt,
				Restarts:  a.Restarts,
				Branch:    a.Branch,
			})
		}
	}

	return agentsData{Agents: agents, Templates: cfg.Templates}, nil
}
