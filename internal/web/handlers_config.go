package web

import (
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
