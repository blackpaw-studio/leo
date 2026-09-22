package web

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
)

type delegationPageData struct {
	Config    *config.DelegationConfig
	Templates []string
	Profiles  []delegationProfileRow
	Rows      []delegationRow
	Warnings  []string
	Records   []any
}

type delegationProfileRow struct {
	Name        string
	Description string
	Active      bool
	RoleCount   int
}

type delegationRole struct {
	Name     string
	UseFor   string
	Declared bool
}

type delegationRow struct {
	Role  delegationRole
	Cells []delegationCell
}

// delegationCell is one autosaving profile×role grid cell. Status is empty,
// "ok", "warn", or "err"; Message explains a non-ok status.
type delegationCell struct {
	Profile        string
	Role           string
	Target         config.RoleTarget
	InheritedModel string
	Templates      []string
	Status         string
	Message        string
}

// delegationStatus is the inline saved/error indicator for autosaved fields.
type delegationStatus struct {
	Status  string
	Message string
}

type delegationPreviewData struct {
	Profile string
	Changes []delegationProfileChange
	Missing []string
}

type delegationProfileChange struct {
	Role   string
	Kind   string
	Before config.RoleTarget
	After  config.RoleTarget
}

func (s *Server) buildDelegationData(_ *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	d := cfg.Delegation
	if d == nil {
		return delegationPageData{}, nil
	}
	templates := sortedTemplateNames(cfg)
	var records []any
	for _, rec := range s.consults.Records() {
		if rec.Role != "" {
			records = append(records, rec)
			if len(records) == 20 {
				break
			}
		}
	}
	profiles := delegationProfileRows(d)
	rows := make([]delegationRow, 0)
	for _, role := range delegationRoles(d) {
		row := delegationRow{Role: role}
		for _, p := range profiles {
			target := d.Profiles[p.Name].Roles[role.Name]
			row.Cells = append(row.Cells, newDelegationCell(cfg, templates, p.Name, role.Name, target))
		}
		rows = append(rows, row)
	}
	return delegationPageData{Config: d, Templates: templates, Profiles: profiles, Rows: rows, Warnings: cfg.DelegationWarnings(), Records: records}, nil
}

func sortedTemplateNames(cfg *config.Config) []string {
	templates := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		templates = append(templates, name)
	}
	sort.Strings(templates)
	return templates
}

func delegationProfileRows(d *config.DelegationConfig) []delegationProfileRow {
	rows := make([]delegationProfileRow, 0, len(d.Profiles))
	for name, p := range d.Profiles {
		rows = append(rows, delegationProfileRow{Name: name, Description: p.Description, Active: name == d.ActiveProfile, RoleCount: len(p.Roles)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// newDelegationCell builds a grid cell whose model placeholder shows the model
// the role would inherit from its template when no override is set.
func newDelegationCell(cfg *config.Config, templates []string, profile, role string, target config.RoleTarget) delegationCell {
	cell := delegationCell{Profile: profile, Role: role, Target: target, Templates: templates}
	if _, ok := cfg.Templates[target.Template]; ok {
		cell.InheritedModel, _ = cfg.RoleTargetModel(config.RoleTarget{Template: target.Template})
	}
	return cell
}

func delegationRoles(d *config.DelegationConfig) []delegationRole {
	all := map[string]delegationRole{}
	for name, spec := range d.Roles {
		all[name] = delegationRole{Name: name, UseFor: spec.UseFor, Declared: true}
	}
	for _, profile := range d.Profiles {
		for name := range profile.Roles {
			if _, declared := all[name]; !declared {
				all[name] = delegationRole{Name: name}
			}
		}
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	roles := make([]delegationRole, 0, len(names))
	for _, name := range names {
		roles = append(roles, all[name])
	}
	return roles
}

// applyDelegation loads, mutates, validates, and saves config, then reloads.
// An invalid edit returns a non-empty errMsg and leaves leo.yaml untouched.
func (s *Server) applyDelegation(apply func(*config.Config) error) (cfg *config.Config, warn, errMsg string) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, "", err.Error()
	}
	if err := apply(cfg); err != nil {
		return nil, "", err.Error()
	}
	if msg := s.validateAndSave(cfg); msg != "" {
		return nil, "", msg
	}
	return cfg, s.reloadConfigOrWarn(), ""
}

// delegationMutation handles structural edits (add/rename/remove, activate):
// on success the page refreshes so the grid, profile badges, and warnings
// all reflect the new shape.
func (s *Server) delegationMutation(w http.ResponseWriter, _ *http.Request, apply func(*config.Config) error) {
	_, warn, errMsg := s.applyDelegation(apply)
	if errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	typ, msg := appendReloadWarning("success", "Delegation saved", warn)
	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, typ, msg)
}

func savedStatus(warn string) delegationStatus {
	if warn != "" {
		return delegationStatus{Status: "warn", Message: "saved — " + warn}
	}
	return delegationStatus{Status: "ok", Message: "saved"}
}

func (s *Server) renderDelegationFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleDelegationCell autosaves one grid cell and swaps just that cell back
// in, so editing never reloads the page or steals focus.
func (s *Server) handleDelegationCell(w http.ResponseWriter, r *http.Request) {
	profile, role := r.FormValue("profile"), r.FormValue("role")
	submitted := config.RoleTarget{Template: r.FormValue("template"), Model: r.FormValue("model"), Effort: r.FormValue("effort")}
	cfg, warn, errMsg := s.applyDelegation(func(cfg *config.Config) error {
		if cfg.Delegation == nil {
			return fmt.Errorf("delegation not configured")
		}
		p, ok := cfg.Delegation.Profiles[profile]
		if !ok {
			return fmt.Errorf("profile %q not found", profile)
		}
		roles := make(map[string]config.RoleTarget, len(p.Roles)+1)
		for name, target := range p.Roles {
			roles[name] = target
		}
		if submitted.Template == "" {
			delete(roles, role)
		} else {
			roles[role] = submitted
		}
		p.Roles = roles
		cfg.Delegation.Profiles[profile] = p
		return nil
	})
	if errMsg != "" {
		fallback, err := s.loadConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cell := newDelegationCell(fallback, sortedTemplateNames(fallback), profile, role, submitted)
		cell.Status, cell.Message = "err", errMsg
		s.renderDelegationFragment(w, "delegation_cell", cell)
		return
	}
	if submitted.Template == "" {
		submitted = config.RoleTarget{}
	}
	cell := newDelegationCell(cfg, sortedTemplateNames(cfg), profile, role, submitted)
	st := savedStatus(warn)
	cell.Status, cell.Message = st.Status, st.Message
	s.renderDelegationFragment(w, "delegation_cell", cell)
}

// handleDelegationUseFor autosaves a role's use_for text and returns only the
// inline status indicator.
func (s *Server) handleDelegationUseFor(w http.ResponseWriter, r *http.Request) {
	_, warn, errMsg := s.applyDelegation(func(cfg *config.Config) error {
		if cfg.Delegation == nil {
			return fmt.Errorf("delegation not configured")
		}
		role := r.FormValue("role")
		spec, ok := cfg.Delegation.Roles[role]
		if !ok {
			return fmt.Errorf("role %q not found", role)
		}
		spec.UseFor = r.FormValue("use_for")
		cfg.Delegation.Roles[role] = spec
		return nil
	})
	st := savedStatus(warn)
	if errMsg != "" {
		st = delegationStatus{Status: "err", Message: errMsg}
	}
	s.renderDelegationFragment(w, "delegation_status", st)
}

func (s *Server) handleDelegationActive(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		if cfg.Delegation == nil {
			return fmt.Errorf("delegation not configured")
		}
		name := r.FormValue("profile")
		if _, ok := cfg.Delegation.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		cfg.Delegation.ActiveProfile = name
		return nil
	})
}

func delegationConfig(cfg *config.Config) (*config.DelegationConfig, error) {
	if cfg.Delegation == nil {
		return nil, fmt.Errorf("delegation not configured")
	}
	return cfg.Delegation, nil
}

func (s *Server) handleDelegationRoleAdd(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := delegationConfig(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		if !validEntityName(name) {
			return errors.New(entityNameError)
		}
		if delegationRoleExists(d, name) {
			return fmt.Errorf("role %q already exists", name)
		}
		template := r.FormValue("template")
		if _, ok := cfg.Templates[template]; !ok {
			return fmt.Errorf("template %q not found", template)
		}
		if d.Roles == nil {
			d.Roles = declaredActiveRoles(d)
		}
		d.Roles[name] = config.RoleSpec{}
		active := d.Profiles[d.ActiveProfile]
		if active.Roles == nil {
			active.Roles = map[string]config.RoleTarget{}
		}
		active.Roles[name] = config.RoleTarget{Template: template}
		d.Profiles[d.ActiveProfile] = active
		return nil
	})
}

// delegationRoleExists reports whether name is declared or mapped in any
// profile, so adding a role can never overwrite existing routing.
func delegationRoleExists(d *config.DelegationConfig, name string) bool {
	if _, ok := d.Roles[name]; ok {
		return true
	}
	for _, p := range d.Profiles {
		if _, ok := p.Roles[name]; ok {
			return true
		}
	}
	return false
}

// declaredActiveRoles builds the first delegation.roles declaration. While
// roles are undeclared the injected block lists the active profile's roles, so
// declaring only a new role would silently drop those; migrate them all.
func declaredActiveRoles(d *config.DelegationConfig) map[string]config.RoleSpec {
	roles := map[string]config.RoleSpec{}
	for name := range d.Profiles[d.ActiveProfile].Roles {
		roles[name] = config.RoleSpec{}
	}
	return roles
}

func (s *Server) handleDelegationRoleRename(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		if !validEntityName(r.FormValue("new_name")) {
			return errors.New(entityNameError)
		}
		return config.RenameRole(cfg, r.FormValue("name"), r.FormValue("new_name"))
	})
}
func (s *Server) handleDelegationRoleDelete(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := delegationConfig(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		found := false
		if _, ok := d.Roles[name]; ok {
			delete(d.Roles, name)
			found = true
		}
		for profileName, p := range d.Profiles {
			if _, ok := p.Roles[name]; ok {
				found = true
			}
			delete(p.Roles, name)
			d.Profiles[profileName] = p
		}
		if !found {
			return fmt.Errorf("role %q not found", name)
		}
		return nil
	})
}
func (s *Server) handleDelegationProfileAdd(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := delegationConfig(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		if !validEntityName(name) {
			return errors.New(entityNameError)
		}
		if _, ok := d.Profiles[name]; ok {
			return fmt.Errorf("profile %q already exists", name)
		}
		d.Profiles[name] = config.Profile{Roles: map[string]config.RoleTarget{}}
		return nil
	})
}
func (s *Server) handleDelegationProfileRename(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := delegationConfig(cfg)
		if err != nil {
			return err
		}
		old, name := r.FormValue("name"), r.FormValue("new_name")
		if !validEntityName(name) {
			return errors.New(entityNameError)
		}
		p, ok := d.Profiles[old]
		if !ok {
			return fmt.Errorf("profile %q not found", old)
		}
		if _, ok := d.Profiles[name]; ok {
			return fmt.Errorf("profile %q already exists", name)
		}
		d.Profiles[name] = p
		delete(d.Profiles, old)
		if d.ActiveProfile == old {
			d.ActiveProfile = name
		}
		return nil
	})
}
func (s *Server) handleDelegationProfileDelete(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := delegationConfig(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		if name == d.ActiveProfile {
			return fmt.Errorf("cannot remove active profile %q", name)
		}
		if _, ok := d.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		delete(d.Profiles, name)
		return nil
	})
}
func (s *Server) handleDelegationProfileDuplicate(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := delegationConfig(cfg)
		if err != nil {
			return err
		}
		source, name := r.FormValue("name"), r.FormValue("new_name")
		if !validEntityName(name) {
			return errors.New(entityNameError)
		}
		p, ok := d.Profiles[source]
		if !ok {
			return fmt.Errorf("profile %q not found", source)
		}
		if _, ok := d.Profiles[name]; ok {
			return fmt.Errorf("profile %q already exists", name)
		}
		roles := map[string]config.RoleTarget{}
		for role, target := range p.Roles {
			roles[role] = target
		}
		p.Roles = roles
		d.Profiles[name] = p
		return nil
	})
}

func (s *Server) handleDelegationPreview(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d, err := delegationConfig(cfg)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	target := r.URL.Query().Get("profile")
	p, ok := d.Profiles[target]
	if !ok {
		http.Error(w, "profile not found", 404)
		return
	}
	current := d.Profiles[d.ActiveProfile]
	diff := config.DiffProfiles(current, p)
	data := delegationPreviewData{Profile: target}
	missing := []string{}
	for role := range d.Roles {
		if _, ok := p.Roles[role]; !ok {
			missing = append(missing, role)
		}
	}
	sort.Strings(missing)
	data.Missing = missing
	for _, role := range diff {
		before, hadBefore := current.Roles[role]
		after, hadAfter := p.Roles[role]
		switch {
		case !hadBefore:
			data.Changes = append(data.Changes, delegationProfileChange{Role: role, Kind: "added", After: after})
		case !hadAfter:
			data.Changes = append(data.Changes, delegationProfileChange{Role: role, Kind: "removed", Before: before})
		default:
			data.Changes = append(data.Changes, delegationProfileChange{Role: role, Kind: "changed", Before: before, After: after})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "delegation_preview", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
