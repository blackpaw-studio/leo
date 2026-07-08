package web

import (
	"fmt"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// fieldView pairs a resolved field value with its select options.
type fieldView struct {
	schema.FieldValue
	Opts []schema.Option
}

// formData feeds components/form.html.
type formData struct {
	Action      string
	Fields      []fieldView
	SubmitLabel string
	DeleteURL   string // optional; renders a delete button
}

// buildForm renders section's registry against target for display. defaults
// (the DefaultsConfig used to compute "inherit" placeholders) is only wired
// up for the sections that actually cascade from it — Defaults, Provider,
// ClientHost, Web, and Client are the top of their own chain (or have no
// notion of inheritance) and must never receive a defaults pointer.
func (s *Server) buildForm(section schema.Section, target any, cfg *config.Config, action string) formData {
	src := schema.OptionSources{Cfg: cfg, Agents: s.agentList}
	var defaults any
	if section != schema.SectionDefaults && section != schema.SectionProvider &&
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

// handleConfigProviderSave is the schema-driven save path for a single
// provider's inline card form. Mirrors handleConfigTemplateSave. Providers
// inject ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN into every process/task/
// template/session that opts into them at launch, so a restart is always
// flagged.
func (s *Server) handleConfigProviderSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionProvider,
		func(cfg *config.Config) (any, bool) {
			p, ok := cfg.Providers[name]
			return &p, ok
		},
		func(cfg *config.Config, v any) { cfg.Providers[name] = *(v.(*config.ProviderConfig)) },
		fmt.Sprintf("Provider %q saved", name), true)
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
	default:
		return "text"
	}
}
