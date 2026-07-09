package web

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/cron"
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

// templateRow is one row of the configured-templates table (pages/config_templates.html).
type templateRow struct {
	Name      string
	Workspace string
	Model     string
	Agent     string
}

// templatesPageData feeds page_config_templates.
type templatesPageData struct {
	Rows []templateRow
}

// buildTemplatesData assembles the templates list: a name-sorted table of
// every configured template. Templates are blueprints for future ephemeral
// agent spawns, not live processes, so unlike buildProcessesData/
// buildTasksData there's no status/history to join in — just the config.
// Cut over to this lightweight shape in Task 9, mirroring buildProcessesData
// (Task 8) and buildTasksData (Task 7).
func (s *Server) buildTemplatesData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	rows := make([]templateRow, 0, len(cfg.Templates))
	for name, tmpl := range cfg.Templates {
		rows = append(rows, templateRow{
			Name:      name,
			Workspace: tmpl.Workspace,
			Model:     tmpl.Model,
			Agent:     tmpl.Agent,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	return templatesPageData{Rows: rows}, nil
}

// processRow is one row of the configured-processes table (pages/processes.html).
type processRow struct {
	Name      string
	Workspace string
	Model     string
	Enabled   bool
}

// processesPageData feeds page_processes. Cards is exactly the shape
// partials/processes.html expects — it's reused unmodified by both the
// initial page render and the 5s poll target (/partials/processes) so the
// card fragment never drifts between the two. Rows backs the table of
// configured processes below the cards.
type processesPageData struct {
	Cards *dashboardData
	Rows  []processRow
}

// buildProcessesData assembles the processes page: the live-status cards
// (via buildDashboardData, which already computes per-process state) plus a
// name-sorted table of every configured process.
func (s *Server) buildProcessesData(r *http.Request) (any, error) {
	dd, err := s.buildDashboardData()
	if err != nil {
		return nil, err
	}

	rows := make([]processRow, 0, len(dd.Config.Processes))
	for name, proc := range dd.Config.Processes {
		rows = append(rows, processRow{
			Name:      name,
			Workspace: proc.Workspace,
			Model:     proc.Model,
			Enabled:   proc.Enabled,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	return processesPageData{Cards: dd, Rows: rows}, nil
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

// hostCard is one entry of settingsPageData.Hosts: a remote-host name paired
// with the schema-driven inline form for its card. Mirrors providerCard.
type hostCard struct {
	Name string
	Form formData
}

// settingsPageData feeds page_config_settings: the Web UI form, the Remote
// client (default_host) form, and the per-host card list plus its add form.
type settingsPageData struct {
	WebForm    formData
	ClientForm formData
	Hosts      []hostCard
}

// buildSettingsData assembles the schema-driven Settings page: Web UI config
// (&cfg.Web), Remote client config (&cfg.Client, default_host only — hosts
// is excluded from SectionClient's registry and rendered separately below),
// and cfg.Client.Hosts as a name-sorted card list, one inline config_form
// per host — same shape as buildProvidersData.
func (s *Server) buildSettingsData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	names := make([]string, 0, len(cfg.Client.Hosts))
	for name := range cfg.Client.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)

	hosts := make([]hostCard, 0, len(names))
	for _, name := range names {
		h := cfg.Client.Hosts[name]
		form := s.buildForm(schema.SectionClientHost, &h, cfg, "/web/config/host/"+url.PathEscape(name))
		form.DeleteURL = "/web/host/" + url.PathEscape(name)
		hosts = append(hosts, hostCard{Name: name, Form: form})
	}

	return settingsPageData{
		WebForm:    s.buildForm(schema.SectionWeb, &cfg.Web, cfg, "/web/config/web"),
		ClientForm: s.buildForm(schema.SectionClient, &cfg.Client, cfg, "/web/config/client"),
		Hosts:      hosts,
	}, nil
}

// providerCard is one entry of providersPageData.Providers: a provider name
// paired with the schema-driven inline form for its card.
type providerCard struct {
	Name string
	Form formData
}

// providersPageData feeds page_config_providers.
type providersPageData struct {
	Providers []providerCard
}

// buildProvidersData assembles a name-sorted card list, one inline
// config_form per provider — unlike processes/tasks/templates, providers
// are few enough that there's no separate list-page-plus-edit-page split;
// every provider's full CRUD form lives right here on /config/providers.
func (s *Server) buildProvidersData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	cards := make([]providerCard, 0, len(names))
	for _, name := range names {
		p := cfg.Providers[name]
		form := s.buildForm(schema.SectionProvider, &p, cfg, "/web/config/provider/"+url.PathEscape(name))
		form.DeleteURL = "/web/provider/" + url.PathEscape(name)
		cards = append(cards, providerCard{Name: name, Form: form})
	}

	return providersPageData{Providers: cards}, nil
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

// taskRow is one row of the tasks list table (pages/tasks.html).
type taskRow struct {
	Name     string
	Schedule string
	NextRun  time.Time
	HasRun   bool
	LastExit int
	Enabled  bool
}

// tasksPageData feeds page_tasks.
type tasksPageData struct {
	Enabled  []taskRow
	Disabled []taskRow
}

// buildTasksData assembles the tasks list: cron next-run times from the
// scheduler and last-exit status from history, keyed by task name.
func (s *Server) buildTasksData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	cronMap := make(map[string]cron.EntryInfo)
	if s.scheduler != nil {
		for _, e := range s.scheduler.List() {
			cronMap[e.Name] = e
		}
	}
	store := s.loadHistory(cfg)

	rows := make([]taskRow, 0, len(cfg.Tasks))
	for name, task := range cfg.Tasks {
		row := taskRow{Name: name, Schedule: task.Schedule, Enabled: task.Enabled}
		if entry, ok := cronMap[name]; ok {
			row.NextRun = entry.Next
		}
		if last := store.Get(name); last != nil {
			row.HasRun = true
			row.LastExit = last.ExitCode
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	enabled := make([]taskRow, 0, len(rows))
	disabled := make([]taskRow, 0, len(rows))
	for _, row := range rows {
		if row.Enabled {
			enabled = append(enabled, row)
		} else {
			disabled = append(disabled, row)
		}
	}

	return tasksPageData{Enabled: enabled, Disabled: disabled}, nil
}

// taskEditData feeds page_task_edit: the schema-driven form over the task's
// config, plus its prompt editor and (via history-{{.Name}}, loaded
// separately by hx-trigger="load") recent runs.
type taskEditData struct {
	Name       string
	PromptFile string
	Form       formData
	Prompt     promptEditorData
}

// handleTaskEditPage renders a single task's edit page: every TaskConfig
// field through the schema-driven form, the prompt editor, and a lazily
// loaded recent-runs panel. Not wired through handlePage because the page
// title is per-task and an unknown name must 404 rather than 500.
func (s *Server) handleTaskEditPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	task, ok := cfg.Tasks[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	form := s.buildForm(schema.SectionTask, &task, cfg, "/web/config/task/"+url.PathEscape(name))
	form.DeleteURL = "/web/task/" + url.PathEscape(name) + "/delete"

	// Best-effort: a task whose configured prompt file path is invalid
	// (e.g. escapes the workspace) still gets an edit page — it just opens
	// with an empty prompt editor rather than a broken page. The dedicated
	// /web/task/{name}/prompt endpoint surfaces the real error via flash.
	prompt, _ := s.buildPromptEditorData(cfg, name, task, "")

	pd := pageData{
		Page:  "task_edit",
		Title: "Task: " + name,
		Data: taskEditData{
			Name:       name,
			PromptFile: task.PromptFile,
			Form:       form,
			Prompt:     prompt,
		},
	}
	if err := s.fillStatus(&pd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "layout.html", pd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// processEditData feeds page_process_edit: the schema-driven form over the
// process's config.
type processEditData struct {
	Name string
	Form formData
}

// handleProcessEditPage renders a single process's edit page: every
// ProcessConfig field through the schema-driven form. Not wired through
// handlePage because the page title is per-process and an unknown name must
// 404 rather than 500. Mirrors handleTaskEditPage.
func (s *Server) handleProcessEditPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	proc, ok := cfg.Processes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	form := s.buildForm(schema.SectionProcess, &proc, cfg, "/web/config/process/"+url.PathEscape(name))
	form.DeleteURL = "/web/process/" + url.PathEscape(name)

	pd := pageData{
		Page:  "process_edit",
		Title: "Process: " + name,
		Data: processEditData{
			Name: name,
			Form: form,
		},
	}
	if err := s.fillStatus(&pd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "layout.html", pd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// serviceData feeds page_service: a name-sorted snapshot of every supervised
// process/agent's live status for the Supervisor table.
type serviceData struct {
	States []ProcessStateInfo
}

// buildServiceData assembles the Service page's supervisor table: every
// entry from the process/agent state provider, sorted by name for
// deterministic rendering (mirrors buildTemplatesData/buildTasksData).
func (s *Server) buildServiceData(r *http.Request) (any, error) {
	var states []ProcessStateInfo
	if s.processes != nil {
		for _, st := range s.processes.States() {
			states = append(states, st)
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })

	return serviceData{States: states}, nil
}

// templateEditData feeds page_template_edit: the schema-driven form over the
// template's config.
type templateEditData struct {
	Name string
	Form formData
}

// handleTemplateEditPage renders a single template's edit page: every
// TemplateConfig field through the schema-driven form. Not wired through
// handlePage because the page title is per-template and an unknown name must
// 404 rather than 500. Mirrors handleProcessEditPage.
func (s *Server) handleTemplateEditPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tmpl, ok := cfg.Templates[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	form := s.buildForm(schema.SectionTemplate, &tmpl, cfg, "/web/config/template/"+url.PathEscape(name))
	form.DeleteURL = "/web/template/" + url.PathEscape(name)

	pd := pageData{
		Page:  "template_edit",
		Title: "Template: " + name,
		Data: templateEditData{
			Name: name,
			Form: form,
		},
	}
	if err := s.fillStatus(&pd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "layout.html", pd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
