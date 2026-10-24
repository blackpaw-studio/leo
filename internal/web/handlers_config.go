package web

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/leotools"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// fieldView pairs a resolved field value with its select options.
type fieldView struct {
	schema.FieldValue
	Opts        []schema.Option
	Section     schema.Section // for the harness select's hx-get URL
	Scope       string         // scope-unique element-id suffix
	ScopeName   string         // raw config map key ("" for defaults); for the harness select's hx-get URL
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
	Section   schema.Section
	Scope     string // scope-unique element-id suffix
	ScopeName string // raw config map key ("" for defaults)
	Harness   string // effective harness the sub-form is rendered for
	Fields    []schema.HarnessFieldValue
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
		section != schema.SectionClient && section != schema.SectionEnvironment {
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
// the model field harness-aware (datalist suggestions / format hint). name is
// the RAW config map key ("" for defaults) — scopeSuffix derives the
// element-id suffix from it so the two can never drift apart.
func (s *Server) buildFormWithHarness(section schema.Section, target any, cfg *config.Config, action, name string) formData {
	scope := scopeSuffix(section, name)
	fd := s.buildForm(section, target, cfg, action)
	fd.Scope = scope
	for i := range fd.Fields {
		fd.Fields[i].Section = section
		fd.Fields[i].Scope = scope
		fd.Fields[i].ScopeName = name
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
		Section:   section,
		Scope:     scope,
		ScopeName: name,
		Harness:   harnessName,
		Fields:    schema.HarnessOptionValues(h, own, inherited, src),
	}
	for i := range fd.Fields {
		if fd.Fields[i].Key == "model" {
			fd.Fields[i].Opts = schema.ModelSuggestions(harnessName)
			fd.Fields[i].Placeholder = schema.ModelPlaceholder(harnessName)
		}
	}
	return fd
}

// permissionFields renders the templates.*.permissions sub-struct as field
// views for the template form. Scope/ScopeName are stamped to match the
// enclosing form's fields so element ids stay unique across templates.
func (s *Server) permissionFields(perms *leotools.Permissions, cfg *config.Config, name string) []fieldView {
	fd := s.buildForm(schema.SectionPermissions, perms, cfg, "")
	scope := scopeSuffix(schema.SectionTemplate, name)
	for i := range fd.Fields {
		fd.Fields[i].Section = schema.SectionPermissions
		fd.Fields[i].Scope = scope
		fd.Fields[i].ScopeName = name
	}
	return fd.Fields
}

// pushFields renders a nested push block (defaults' or a template's) as field
// views for its parent form. scope and name stamp the fields the same way the
// parent form's are, so element ids stay unique.
func (s *Server) pushFields(section schema.Section, push *config.PushConfig, cfg *config.Config, scope, name string) []fieldView {
	fd := s.buildForm(section, push, cfg, "")
	for i := range fd.Fields {
		fd.Fields[i].Section = section
		fd.Fields[i].Scope = scope
		fd.Fields[i].ScopeName = name
	}
	return fd.Fields
}

// harnessView resolves a form target's own options map, effective harness,
// and the inherited-placeholder map per the cascade rules (mirrors
// config.scopeHarnessOptions: defaults' options cascade only into scopes
// running the same harness; defaults itself never shows inherited
// placeholders).
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
	case *config.TaskConfig:
		name = cfg.TaskHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	case *config.TemplateConfig:
		name = cfg.TemplateHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	}
	return nil, config.DefaultHarnessName, nil
}

// applyResource is the HTML adapter over ConfigService.ApplyValues: it parses
// the posted form, saves it into the named item of res and flashes the
// outcome. Validation, persistence and the restart banner live in the core, so
// the JSON API saves identically.
func (s *Server) applyResource(w http.ResponseWriter, r *http.Request, res *configResource, name string) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", "Invalid form: "+err.Error())
		return
	}
	result, err := s.ApplyValues("", res, name, r.Form)
	if err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	typ, msg := appendReloadWarning("success", res.saved(name), result.Warning)
	s.renderFlash(w, typ, msg)
}

// applyScopeHarnessOptions decodes the harness_options.* inputs in form
// against harnessName's options schema. harnessName is expected to be
// resolved from the scope's already-Apply-updated target (via its cascade
// helper, e.g. cfg.TemplateHarness), so a harness change and its options
// land atomically in one save.
func applyScopeHarnessOptions(form url.Values, harnessName string) (map[string]any, error) {
	h, err := harness.Get(harnessName)
	if err != nil {
		return nil, fmt.Errorf("harness %q is not registered", harnessName)
	}
	return schema.ApplyHarnessOptions(h, form)
}

// The save handlers below are one line each: which resource, which item. What
// a save does (and which restart banner it raises) is the resource's, see
// configResources. Defaults changes affect every process, task, template and
// session that inherits from them, so a running agent keeps its spawn-time
// args until restarted; Web port/bind/allowed_hosts only apply when the
// daemon's TCP listener is rebuilt; Tasks, the Remote client and hosts need
// only the reload.

func (s *Server) handleConfigDefaultsSave(w http.ResponseWriter, r *http.Request) {
	s.applyResource(w, r, resourceByKey("defaults"), "")
}

func (s *Server) handleConfigTaskSave(w http.ResponseWriter, r *http.Request) {
	s.applyResource(w, r, resourceByKey("tasks"), r.PathValue("name"))
}

func (s *Server) handleConfigTemplateSave(w http.ResponseWriter, r *http.Request) {
	s.applyResource(w, r, resourceByKey("templates"), r.PathValue("name"))
}

func (s *Server) handleConfigWebSave(w http.ResponseWriter, r *http.Request) {
	s.applyResource(w, r, resourceByKey("web"), "")
}

func (s *Server) handleConfigClientSave(w http.ResponseWriter, r *http.Request) {
	s.applyResource(w, r, resourceByKey("client"), "")
}

func (s *Server) handleConfigHostSave(w http.ResponseWriter, r *http.Request) {
	s.applyResource(w, r, resourceByKey("hosts"), r.PathValue("name"))
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
