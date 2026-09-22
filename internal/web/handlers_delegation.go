package web

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
)

type delegationPageData struct {
	Config    *config.DelegationConfig
	Templates []delegationTemplate
	Profiles  []delegationProfileRow
	Rows      []delegationRow
	Warnings  []string
	Records   []any
}

// delegationTemplate is a template choice plus the model a role inherits
// from it, so the grid can update the model placeholder without a swap.
type delegationTemplate struct {
	Name  string
	Model string
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
	StatusID string
}

type delegationRow struct {
	Role  delegationRole
	Cells []delegationCell
}

// delegationStatus is the inline saved/error indicator of an autosaved field.
// ID addresses the field's message slot; Status is "", "ok", "warn", or "err".
type delegationStatus struct {
	ID      string
	Status  string
	Message string
}

// delegationCell is one autosaving profile×role grid cell.
type delegationCell struct {
	delegationStatus
	Profile        string
	Role           string
	Target         config.RoleTarget
	InheritedModel string
	Templates      []delegationTemplate
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

// Element ids built from names hex-encode them: names may contain '.', which
// is not safe in a CSS selector, and hex never collides the way escaping can.
func cellStatusID(profile, role string) string {
	return "dc-" + hex.EncodeToString([]byte(profile)) + "-" + hex.EncodeToString([]byte(role))
}

func useForStatusID(role string) string { return "uf-" + hex.EncodeToString([]byte(role)) }

func (s *Server) buildDelegationData(_ *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	d := cfg.Delegation
	if d == nil {
		return delegationPageData{}, nil
	}
	templates := delegationTemplates(cfg)
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
			row.Cells = append(row.Cells, newDelegationCell(cfg, templates, p.Name, role.Name, d.Profiles[p.Name].Roles[role.Name]))
		}
		rows = append(rows, row)
	}
	return delegationPageData{Config: d, Templates: templates, Profiles: profiles, Rows: rows, Warnings: cfg.DelegationWarnings(), Records: records}, nil
}

func delegationTemplates(cfg *config.Config) []delegationTemplate {
	templates := make([]delegationTemplate, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		model, _ := cfg.RoleTargetModel(config.RoleTarget{Template: name})
		templates = append(templates, delegationTemplate{Name: name, Model: model})
	}
	sort.Slice(templates, func(i, j int) bool { return templates[i].Name < templates[j].Name })
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
func newDelegationCell(cfg *config.Config, templates []delegationTemplate, profile, role string, target config.RoleTarget) delegationCell {
	cell := delegationCell{delegationStatus: delegationStatus{ID: cellStatusID(profile, role)}, Profile: profile, Role: role, Target: target, Templates: templates}
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
		role := all[name]
		role.StatusID = useForStatusID(name)
		roles = append(roles, role)
	}
	return roles
}

// delegationMutation handles structural edits (add/rename/remove, activate):
// on success the page refreshes so the grid, profile badges, and warnings
// all reflect the new shape.
func (s *Server) delegationMutation(w http.ResponseWriter, _ *http.Request, apply func(*config.Config) error) {
	warn, errMsg := s.mutateConfig(apply)
	if errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	typ, msg := appendReloadWarning("success", "Delegation saved", warn)
	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, typ, msg)
}

// renderDelegationStatus answers an autosave with only its status indicator
// and an out-of-band message slot, so the user's inputs are never replaced.
func (s *Server) renderDelegationStatus(w http.ResponseWriter, id, warn, errMsg string) {
	st := delegationStatus{ID: id, Status: "ok", Message: "saved"}
	switch {
	case errMsg != "":
		st.Status, st.Message = "err", errMsg
	case warn != "":
		st.Status, st.Message = "warn", "saved — "+warn
	}
	s.renderDelegationFragment(w, "delegation_status", st)
}

func (s *Server) renderDelegationFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleDelegationCell autosaves one grid cell.
func (s *Server) handleDelegationCell(w http.ResponseWriter, r *http.Request) {
	profile, role := r.FormValue("profile"), r.FormValue("role")
	submitted := config.RoleTarget{Template: r.FormValue("template"), Model: r.FormValue("model"), Effort: r.FormValue("effort")}
	warn, errMsg := s.mutateConfig(func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		p, ok := d.Profiles[profile]
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
		d.Profiles[profile] = p
		return nil
	})
	s.renderDelegationStatus(w, cellStatusID(profile, role), warn, errMsg)
}

// handleDelegationUseFor autosaves a declared role's use_for text.
func (s *Server) handleDelegationUseFor(w http.ResponseWriter, r *http.Request) {
	role := r.FormValue("role")
	warn, errMsg := s.mutateConfig(func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		spec, ok := d.Roles[role]
		if !ok {
			return fmt.Errorf("role %q not found", role)
		}
		spec.UseFor = r.FormValue("use_for")
		d.Roles[role] = spec
		return nil
	})
	s.renderDelegationStatus(w, useForStatusID(role), warn, errMsg)
}

func (s *Server) handleDelegationActive(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("profile")
		if _, ok := d.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		d.ActiveProfile = name
		return nil
	})
}

func (s *Server) handleDelegationPreview(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d, err := requireDelegation(cfg)
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
