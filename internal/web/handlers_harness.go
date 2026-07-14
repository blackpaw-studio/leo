package web

import (
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// harnessPartialData feeds harness_options_partial.html: the sub-form for
// the newly selected harness plus the OOB datalist refresh for the model
// input (a stale datalist is harmless — ValidateModel gates on save — but
// refreshing it keeps suggestions honest).
type harnessPartialData struct {
	Form      harnessFormData
	ModelOpts []schema.Option
}

// handleHarnessOptionsPartial re-renders the harness-options sub-form when
// a form's harness dropdown changes. Stored option values render only when
// the selected harness matches the scope's stored effective harness —
// switching harnesses starts from a blank slate (the stored map still
// belongs to the old harness until the user saves).
func (s *Server) handleHarnessOptionsPartial(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	section := schema.Section(q.Get("section"))
	scopeName := q.Get("scope")
	selected := q.Get("harness")

	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	target, scope, ok := locateHarnessScope(cfg, section, scopeName)
	if !ok {
		http.Error(w, "unknown section or scope", http.StatusNotFound)
		return
	}

	stored, storedName, _ := harnessView(target, cfg)
	name := selected
	if name == "" {
		name = cfg.DefaultsHarness()
	}
	h, err := harness.Get(name)
	if err != nil {
		http.Error(w, "unknown harness", http.StatusBadRequest)
		return
	}
	if name != storedName {
		stored = nil // blank slate across harnesses
	}
	// Recompute the inherited-placeholder map against the SELECTED harness
	// (harnessView computed it against the stored one — switching TO the
	// defaults harness must light the placeholders up, and away must drop
	// them). The defaults form itself never shows any.
	var inherited map[string]any
	if section != schema.SectionDefaults && name == cfg.DefaultsHarness() {
		inherited = cfg.Defaults.HarnessOptions
	}

	src := schema.OptionSources{Cfg: cfg, Agents: s.agentList}
	data := harnessPartialData{
		Form: harnessFormData{Section: section, Scope: scope, ScopeName: scopeName, Harness: name,
			Fields: schema.HarnessOptionValues(h, stored, inherited, src)},
		ModelOpts: schema.ModelSuggestions(name),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "harness_options_partial", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// scopeSuffix returns the scope-unique element-id suffix for a section's
// form ("defaults", "process-<name>", ...). Shared by buildFormWithHarness
// and locateHarnessScope so URL params (raw config map keys) and element ids
// (this suffix) can't drift apart.
func scopeSuffix(section schema.Section, name string) string {
	if section == schema.SectionDefaults {
		return "defaults"
	}
	return string(section) + "-" + name
}

// locateHarnessScope maps a (section, scope-name) pair to the config struct
// backing its form, plus the scope id suffix used for element ids. name is
// the RAW config map key ("" for defaults).
func locateHarnessScope(cfg *config.Config, section schema.Section, name string) (any, string, bool) {
	switch section {
	case schema.SectionDefaults:
		return &cfg.Defaults, scopeSuffix(section, name), true
	case schema.SectionTask:
		if t, ok := cfg.Tasks[name]; ok {
			return &t, scopeSuffix(section, name), true
		}
	case schema.SectionTemplate:
		if t, ok := cfg.Templates[name]; ok {
			return &t, scopeSuffix(section, name), true
		}
	}
	return nil, "", false
}
