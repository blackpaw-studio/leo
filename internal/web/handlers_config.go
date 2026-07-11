package web

import (
	"fmt"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// fieldView pairs a resolved field value with its select options.
type fieldView struct {
	schema.FieldValue
	Opts        []schema.Option
	Section     schema.Section // for the harness select's hx-get URL
	Scope       string         // scope-unique element-id suffix
	Placeholder string         // per-harness model format hint
}

// formData feeds components/form.html.
type formData struct {
	Action      string
	Scope       string
	Fields      []fieldView
	Harness     *harnessFormData // nil = section has no harness sub-form
	SubmitLabel string
	DeleteURL   string // optional; renders a delete button
}

// harnessFormData feeds components/harness_options.html: the harness_options
// sub-form for a single config scope.
type harnessFormData struct {
	Section schema.Section
	Scope   string
	Harness string // effective harness the sub-form is rendered for
	Fields  []schema.HarnessFieldValue
}

// buildForm renders section's registry against target for display. defaults
// (the DefaultsConfig used to compute "inherit" placeholders) is only wired
// up for the sections that actually cascade from it — Defaults, ClientHost,
// Web, and Client are the top of their own chain (or have no notion of
// inheritance) and must never receive a defaults pointer.
func (s *Server) buildForm(section schema.Section, target any, cfg *config.Config, action string) formData {
	src := schema.OptionSources{Cfg: cfg, Agents: s.agentList}
	var defaults any
	if section != schema.SectionDefaults &&
		section != schema.SectionClientHost && section != schema.SectionWeb &&
		section != schema.SectionClient {
		defaults = &cfg.Defaults
	}
	fd := formData{Action: action, SubmitLabel: "Save"}
	for _, fv := range schema.Values(target, section, defaults) {
		view := fieldView{FieldValue: fv}
		if fv.Options != "" {
			view.Opts = src.For(fv.Options)
		}
		fd.Fields = append(fd.Fields, view)
	}
	return fd
}

// buildFormWithHarness wraps buildForm for the five config sections that
// carry harness/harness_options: it threads a scope-unique id suffix,
// resolves the effective harness, attaches the options sub-form, and makes
// the model field harness-aware (datalist suggestions / format hint).
func (s *Server) buildFormWithHarness(section schema.Section, target any, cfg *config.Config, action, scope string) formData {
	fd := s.buildForm(section, target, cfg, action)
	fd.Scope = scope
	for i := range fd.Fields {
		fd.Fields[i].Section = section
		fd.Fields[i].Scope = scope
	}

	own, harnessName, inherited := harnessView(target, cfg)
	h, err := harness.Get(harnessName)
	if err != nil {
		// Unregistered harness in a hand-edited config: render the flat form
		// without a sub-form rather than 500ing the page; Validate() reports
		// the real error on save.
		return fd
	}
	src := schema.OptionSources{Cfg: cfg, Agents: s.agentList}
	fd.Harness = &harnessFormData{
		Section: section,
		Scope:   scope,
		Harness: harnessName,
		Fields:  schema.HarnessOptionValues(h, own, inherited, src),
	}
	for i := range fd.Fields {
		if fd.Fields[i].Key == "model" {
			fd.Fields[i].Opts = schema.ModelSuggestions(harnessName)
			fd.Fields[i].Placeholder = schema.ModelPlaceholder(harnessName)
		}
	}
	return fd
}

// harnessView resolves a form target's own options map, effective harness,
// and the inherited-placeholder map per the cascade rules (mirrors
// config.scopeHarnessOptions: defaults' options cascade only into scopes
// running the same harness; sessions and defaults itself never show
// inherited placeholders).
func harnessView(target any, cfg *config.Config) (own map[string]any, name string, inherited map[string]any) {
	sameHarnessDefaults := func(n string) map[string]any {
		if n == cfg.DefaultsHarness() {
			return cfg.Defaults.HarnessOptions
		}
		return nil
	}
	switch v := target.(type) {
	case *config.DefaultsConfig:
		return v.HarnessOptions, cfg.DefaultsHarness(), nil
	case *config.ProcessConfig:
		name = cfg.ProcessHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	case *config.TaskConfig:
		name = cfg.TaskHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	case *config.TemplateConfig:
		name = cfg.TemplateHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	case *config.SessionConfig:
		return v.HarnessOptions, cfg.SessionHarness(*v), nil
	}
	return nil, config.DefaultHarnessName, nil
}

// applySection is the single save path for every schema-driven config form.
// locate returns a pointer to the section's struct living inside cfg (or a
// copy to be written back via put); put writes the (mutated) value back into
// cfg. needsRestart marks process-affecting sections so the restart banner
// appears after a successful save.
func (s *Server) applySection(w http.ResponseWriter, r *http.Request,
	section schema.Section,
	locate func(cfg *config.Config) (any, bool),
	put func(cfg *config.Config, v any),
	okMsg string, needsRestart bool,
) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", "Invalid form: "+err.Error())
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", "Failed to load config: "+err.Error())
		return
	}
	target, ok := locate(cfg)
	if !ok {
		s.renderFlash(w, "error", "Not found")
		return
	}
	if err := schema.Apply(target, section, r.Form); err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	put(cfg, target)
	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	warn := s.reloadConfigOrWarn()
	if needsRestart {
		s.restartNeeded.Store(true)
	}
	typ, msg := appendReloadWarning("success", okMsg, warn)
	s.renderFlash(w, typ, msg)
}

// handleConfigDefaultsSave is the schema-driven replacement for the old
// hand-rolled handleConfigDefaults. Defaults changes affect every process,
// task, template, and session that inherits from them, so a restart is
// always flagged.
func (s *Server) handleConfigDefaultsSave(w http.ResponseWriter, r *http.Request) {
	s.applySection(w, r, schema.SectionDefaults,
		func(cfg *config.Config) (any, bool) { return &cfg.Defaults, true },
		func(cfg *config.Config, v any) {}, // &cfg.Defaults is already the live field — nothing to write back
		"Defaults saved", true)
}

// handleConfigTaskSave is the schema-driven replacement for the old
// hand-rolled handleConfigTask. Tasks don't affect running processes, so no
// restart flag is set.
func (s *Server) handleConfigTaskSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionTask,
		func(cfg *config.Config) (any, bool) {
			t, ok := cfg.Tasks[name]
			return &t, ok
		},
		func(cfg *config.Config, v any) { cfg.Tasks[name] = *(v.(*config.TaskConfig)) },
		fmt.Sprintf("Task %q saved", name), false)
}

// handleConfigProcessSave is the schema-driven replacement for the old
// hand-rolled handleConfigProcess. It also replaces that handler's
// bypass_permissions special-case (which cleared the field to a concrete
// false whenever permission_mode was empty, and never let the web UI submit
// true) with the tri-state inherit/true/false the schema form now renders.
// Processes affect running sessions, so a restart is always flagged.
func (s *Server) handleConfigProcessSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionProcess,
		func(cfg *config.Config) (any, bool) {
			p, ok := cfg.Processes[name]
			return &p, ok
		},
		func(cfg *config.Config, v any) { cfg.Processes[name] = *(v.(*config.ProcessConfig)) },
		fmt.Sprintf("Process %q saved", name), true)
}

// handleConfigTemplateSave is the schema-driven replacement for the old
// hand-rolled handleConfigTemplate. It also fixes that handler's silent
// omission of provider and idle_suspend_after (added to TemplateConfig since
// the old handler was written, but never wired into its field-by-field
// parsing). Templates only affect future agent spawns, not running
// processes, so no restart flag is set.
func (s *Server) handleConfigTemplateSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionTemplate,
		func(cfg *config.Config) (any, bool) {
			t, ok := cfg.Templates[name]
			return &t, ok
		},
		func(cfg *config.Config, v any) { cfg.Templates[name] = *(v.(*config.TemplateConfig)) },
		fmt.Sprintf("Template %q saved", name), false)
}

// handleConfigWebSave is the schema-driven save path for the Web UI card on
// /config/settings. cfg.Web is directly addressable (like cfg.Defaults), so
// this follows handleConfigDefaultsSave's no-op-put pattern rather than the
// map-entry copy-then-write-back pattern hosts use. Port/bind/allowed_hosts
// changes can affect how the running web server is reachable, so a restart
// is always flagged (matches the schema.SectionWeb Warning strings on those
// fields).
func (s *Server) handleConfigWebSave(w http.ResponseWriter, r *http.Request) {
	s.applySection(w, r, schema.SectionWeb,
		func(cfg *config.Config) (any, bool) { return &cfg.Web, true },
		func(cfg *config.Config, v any) {}, // &cfg.Web is already the live field — nothing to write back
		"Web UI settings saved", true)
}

// handleConfigClientSave is the schema-driven save path for the Remote
// client card (default_host only — SectionClient's registry excludes
// "hosts", which gets its own map-CRUD UI below). default_host only affects
// future `leo agent` client dispatch, not the running service, so no
// restart is flagged.
func (s *Server) handleConfigClientSave(w http.ResponseWriter, r *http.Request) {
	s.applySection(w, r, schema.SectionClient,
		func(cfg *config.Config) (any, bool) { return &cfg.Client, true },
		func(cfg *config.Config, v any) {}, // &cfg.Client is already the live field — nothing to write back
		"Remote client settings saved", false)
}

// handleConfigHostSave is the schema-driven save path for a single remote
// host's inline card form. Uses the same map-entry copy-then-write-back
// shape as handleConfigSessionSave. Host connection details (ssh, ssh_args,
// leo_path, tmux_path) only affect future `leo agent` dispatch to that host,
// not the running service, so no restart is flagged.
func (s *Server) handleConfigHostSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionClientHost,
		func(cfg *config.Config) (any, bool) {
			h, ok := cfg.Client.Hosts[name]
			return &h, ok
		},
		func(cfg *config.Config, v any) { cfg.Client.Hosts[name] = *(v.(*config.HostConfig)) },
		fmt.Sprintf("Host %q saved", name), false)
}

// handleConfigSessionSave is the schema-driven save path for a single
// persistent session's inline card form. Uses the same map-entry
// copy-then-write-back shape as handleConfigHostSave. Sessions boot lazily
// on first use — there is no long-running process for a config change to
// invalidate — so unlike processes this never flags a restart.
func (s *Server) handleConfigSessionSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionSession,
		func(cfg *config.Config) (any, bool) {
			sc, ok := cfg.Sessions[name]
			return &sc, ok
		},
		func(cfg *config.Config, v any) { cfg.Sessions[name] = *(v.(*config.SessionConfig)) },
		fmt.Sprintf("Session %q saved", name), false)
}

// kindName maps a resolved schema.Kind to the string components/form.html
// switches on, so templates compare readable names instead of brittle raw
// integers.
func kindName(k schema.Kind) string {
	switch k {
	case schema.KindText:
		return "text"
	case schema.KindNumber:
		return "number"
	case schema.KindBool:
		return "bool"
	case schema.KindTriBool:
		return "tribool"
	case schema.KindSelect:
		return "select"
	case schema.KindCSV:
		return "csv"
	case schema.KindEnvMap:
		return "envmap"
	case schema.KindCron:
		return "cron"
	case schema.KindDuration:
		return "duration"
	case schema.KindTextarea:
		return "textarea"
	case schema.KindDatalist:
		return "datalist"
	default:
		return "text"
	}
}

// optTypeName maps a harness.OptionType to the string
// components/harness_options.html switches on.
func optTypeName(t harness.OptionType) string {
	switch t {
	case harness.OptionBool:
		return "bool"
	case harness.OptionEnum:
		return "enum"
	case harness.OptionStringList:
		return "list"
	case harness.OptionYAMLMap:
		return "yamlmap"
	case harness.OptionText:
		return "text"
	default:
		return "string"
	}
}
