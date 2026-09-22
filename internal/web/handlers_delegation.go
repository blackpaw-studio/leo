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
	Roles     []delegationRole
	Warnings  []string
	Records   []any
}

type delegationRole struct {
	Name     string
	UseFor   string
	Declared bool
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
	templates := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		templates = append(templates, name)
	}
	sort.Strings(templates)
	var records []any
	for _, rec := range s.consults.Records() {
		if rec.Role != "" {
			records = append(records, rec)
			if len(records) == 20 {
				break
			}
		}
	}
	roles := delegationRoles(d)
	return delegationPageData{Config: d, Templates: templates, Roles: roles, Warnings: cfg.DelegationWarnings(), Records: records}, nil
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

func (s *Server) delegationMutation(w http.ResponseWriter, r *http.Request, apply func(*config.Config) error) {
	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	if err := apply(cfg); err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	if msg := s.validateAndSave(cfg); msg != "" {
		s.renderFlash(w, "error", msg)
		return
	}
	warn := s.reloadConfigOrWarn()
	typ, msg := appendReloadWarning("success", "Delegation saved", warn)
	// Match existing config pages: a successful mutation must refresh the
	// grid, profile markers, and warning state, not only its flash message.
	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, typ, msg)
}

func (s *Server) handleDelegationCell(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		if cfg.Delegation == nil {
			return fmt.Errorf("delegation not configured")
		}
		profile, role := r.FormValue("profile"), r.FormValue("role")
		p, ok := cfg.Delegation.Profiles[profile]
		if !ok {
			return fmt.Errorf("profile %q not found", profile)
		}
		template := r.FormValue("template")
		if template == "" {
			delete(p.Roles, role)
		} else {
			if p.Roles == nil {
				p.Roles = map[string]config.RoleTarget{}
			}
			p.Roles[role] = config.RoleTarget{Template: template, Model: r.FormValue("model"), Effort: r.FormValue("effort")}
		}
		cfg.Delegation.Profiles[profile] = p
		return nil
	})
}

func (s *Server) handleDelegationUseFor(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
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
		if _, ok := d.Roles[name]; ok {
			return fmt.Errorf("role %q already exists", name)
		}
		if d.Roles == nil {
			d.Roles = map[string]config.RoleSpec{}
		}
		template := r.FormValue("template")
		if _, ok := cfg.Templates[template]; !ok {
			return fmt.Errorf("template %q not found", template)
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
