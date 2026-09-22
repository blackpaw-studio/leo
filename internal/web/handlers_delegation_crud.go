package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
)

func requireDelegation(cfg *config.Config) (*config.DelegationConfig, error) {
	if cfg.Delegation == nil {
		return nil, fmt.Errorf("delegation not configured")
	}
	return cfg.Delegation, nil
}

// checkNewDelegationName is the shared preamble of every add/rename/duplicate:
// the name must pass the shared validator and must not already exist.
func checkNewDelegationName(kind, name string, exists bool) error {
	if !validEntityName(name) {
		return errors.New(entityNameError)
	}
	if exists {
		return fmt.Errorf("%s %q already exists", kind, name)
	}
	return nil
}

func hasProfile(d *config.DelegationConfig, name string) bool {
	_, ok := d.Profiles[name]
	return ok
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

func (s *Server) handleDelegationRoleAdd(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		if err := checkNewDelegationName("role", name, d.HasRole(name)); err != nil {
			return err
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

func (s *Server) handleDelegationRoleRename(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("new_name")
		if err := checkNewDelegationName("role", name, d.HasRole(name)); err != nil {
			return err
		}
		return config.RenameRole(cfg, r.FormValue("name"), name)
	})
}

// handleDelegationRoleDelete removes a role everywhere. Removing the last
// declared role is refused: an empty roles map is dropped on save, which would
// silently switch the injected block to fallback mode.
func (s *Server) handleDelegationRoleDelete(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		if !d.HasRole(name) {
			return fmt.Errorf("role %q not found", name)
		}
		if _, declared := d.Roles[name]; declared && len(d.Roles) == 1 {
			return fmt.Errorf("cannot remove the last declared role %q; declare another role first", name)
		}
		delete(d.Roles, name)
		for profileName, p := range d.Profiles {
			delete(p.Roles, name)
			d.Profiles[profileName] = p
		}
		return nil
	})
}

func (s *Server) handleDelegationProfileAdd(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		if err := checkNewDelegationName("profile", name, hasProfile(d, name)); err != nil {
			return err
		}
		d.Profiles[name] = config.Profile{Roles: map[string]config.RoleTarget{}}
		return nil
	})
}

func (s *Server) handleDelegationProfileRename(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		old, name := r.FormValue("name"), r.FormValue("new_name")
		if err := checkNewDelegationName("profile", name, hasProfile(d, name)); err != nil {
			return err
		}
		p, ok := d.Profiles[old]
		if !ok {
			return fmt.Errorf("profile %q not found", old)
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
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		name := r.FormValue("name")
		if name == d.ActiveProfile {
			return fmt.Errorf("cannot remove active profile %q", name)
		}
		if !hasProfile(d, name) {
			return fmt.Errorf("profile %q not found", name)
		}
		delete(d.Profiles, name)
		return nil
	})
}

func (s *Server) handleDelegationProfileDuplicate(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		source, name := r.FormValue("name"), r.FormValue("new_name")
		if err := checkNewDelegationName("profile", name, hasProfile(d, name)); err != nil {
			return err
		}
		p, ok := d.Profiles[source]
		if !ok {
			return fmt.Errorf("profile %q not found", source)
		}
		roles := make(map[string]config.RoleTarget, len(p.Roles))
		for role, target := range p.Roles {
			roles[role] = target
		}
		p.Roles = roles
		d.Profiles[name] = p
		return nil
	})
}
