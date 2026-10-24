package web

import (
	"fmt"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Delegation edits are Mutate apply funcs built from plain arguments, so the
// HTML forms and the JSON routes share them: each adapter only decides where
// the arguments come from and how the outcome is reported.

type delegationMutator = func(*config.Config) error

func requireDelegation(cfg *config.Config) (*config.DelegationConfig, error) {
	if cfg.Delegation == nil {
		return nil, &ConfigError{Status: http.StatusNotFound, Code: CodeDelegationMissing, Message: "delegation not configured"}
	}
	return cfg.Delegation, nil
}

// checkNewDelegationName is the shared preamble of every add/rename/duplicate:
// the name must pass the shared validator and must not already exist. field
// is the request key a failure lands on ("name" or "new_name").
func checkNewDelegationName(field, kind, name string, exists bool) error {
	if !validEntityName(name) {
		return errInvalid(field, entityNameError)
	}
	if exists {
		return errInvalid(field, fmt.Sprintf("%s %q already exists", kind, name))
	}
	return nil
}

func hasProfile(d *config.DelegationConfig, name string) bool {
	_, ok := d.Profiles[name]
	return ok
}

func profileNotFound(name string) error { return errNotFound("profile %q not found", name) }
func roleNotFound(name string) error    { return errNotFound("role %q not found", name) }

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

// withDelegation adapts f to a mutator that first requires a delegation block.
func withDelegation(f func(cfg *config.Config, d *config.DelegationConfig) error) delegationMutator {
	return func(cfg *config.Config) error {
		d, err := requireDelegation(cfg)
		if err != nil {
			return err
		}
		return f(cfg, d)
	}
}

// setCell writes one profile × role cell; an empty template deletes it.
func setCell(profile, role string, target config.RoleTarget) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		p, ok := d.Profiles[profile]
		if !ok {
			return profileNotFound(profile)
		}
		roles := make(map[string]config.RoleTarget, len(p.Roles)+1)
		for name, t := range p.Roles {
			roles[name] = t
		}
		if target.Template == "" {
			delete(roles, role)
		} else {
			roles[role] = target
		}
		p.Roles = roles
		d.Profiles[profile] = p
		return nil
	})
}

// setUseFor writes a declared role's use_for text.
func setUseFor(role, useFor string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		spec, ok := d.Roles[role]
		if !ok {
			return roleNotFound(role)
		}
		spec.UseFor = useFor
		d.Roles[role] = spec
		return nil
	})
}

// setHiddenAgents records the native agent types bridged claude agents may not
// spawn while delegation is on.
func setHiddenAgents(names []string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		d.SetHiddenNativeAgents(names)
		return nil
	})
}

// setEnabled flips the global delegation switch. Profiles and roles are kept
// either way.
func setEnabled(on bool) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		d.SetEnabled(on)
		return nil
	})
}

func setActiveProfile(name string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		if !hasProfile(d, name) {
			return profileNotFound(name)
		}
		d.ActiveProfile = name
		return nil
	})
}

func addRole(name, template string) delegationMutator {
	return withDelegation(func(cfg *config.Config, d *config.DelegationConfig) error {
		if err := checkNewDelegationName("name", "role", name, d.HasRole(name)); err != nil {
			return err
		}
		if _, ok := cfg.Templates[template]; !ok {
			return errInvalid("template", fmt.Sprintf("template %q not found", template))
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

func renameRole(name, newName string) delegationMutator {
	return withDelegation(func(cfg *config.Config, d *config.DelegationConfig) error {
		if err := checkNewDelegationName("new_name", "role", newName, d.HasRole(newName)); err != nil {
			return err
		}
		if !d.HasRole(name) {
			return roleNotFound(name)
		}
		if err := config.RenameRole(cfg, name, newName); err != nil {
			return errInvalid("new_name", err.Error())
		}
		return nil
	})
}

// deleteRole removes a role everywhere. Removing the last declared role is
// refused: an empty roles map is dropped on save, which would silently switch
// the injected block to fallback mode.
func deleteRole(name string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		if !d.HasRole(name) {
			return roleNotFound(name)
		}
		if _, declared := d.Roles[name]; declared && len(d.Roles) == 1 {
			return errInvalid("", fmt.Sprintf("cannot remove the last declared role %q; declare another role first", name))
		}
		delete(d.Roles, name)
		for profileName, p := range d.Profiles {
			delete(p.Roles, name)
			d.Profiles[profileName] = p
		}
		return nil
	})
}

func addProfile(name string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		if err := checkNewDelegationName("name", "profile", name, hasProfile(d, name)); err != nil {
			return err
		}
		d.Profiles[name] = config.Profile{Roles: map[string]config.RoleTarget{}}
		return nil
	})
}

func renameProfile(old, name string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		if err := checkNewDelegationName("new_name", "profile", name, hasProfile(d, name)); err != nil {
			return err
		}
		p, ok := d.Profiles[old]
		if !ok {
			return profileNotFound(old)
		}
		d.Profiles[name] = p
		delete(d.Profiles, old)
		if d.ActiveProfile == old {
			d.ActiveProfile = name
		}
		return nil
	})
}

func deleteProfile(name string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		if name == d.ActiveProfile {
			return errInvalid("", fmt.Sprintf("cannot remove active profile %q", name))
		}
		if !hasProfile(d, name) {
			return profileNotFound(name)
		}
		delete(d.Profiles, name)
		return nil
	})
}

func duplicateProfile(source, name string) delegationMutator {
	return withDelegation(func(_ *config.Config, d *config.DelegationConfig) error {
		if err := checkNewDelegationName("new_name", "profile", name, hasProfile(d, name)); err != nil {
			return err
		}
		p, ok := d.Profiles[source]
		if !ok {
			return profileNotFound(source)
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
