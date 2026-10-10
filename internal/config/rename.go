package config

import (
	"fmt"
	"maps"
	"slices"
)

// RenameTemplate re-keys a template from oldName to newName within cfg and
// rewrites every task that targets the old name, so no reference is left
// dangling. It mutates cfg in place.
//
// Name-shape validation is the caller's responsibility (the web layer applies
// validEntityName before calling this, and the config package must not import
// web). RenameTemplate guards only the structural invariants it can see:
// non-empty new name, old exists, new does not collide.
func RenameTemplate(cfg *Config, oldName, newName string) error {
	if newName == "" {
		return fmt.Errorf("new template name must not be empty")
	}
	tmpl, ok := cfg.Templates[oldName]
	if !ok {
		return fmt.Errorf("template %q not found", oldName)
	}
	if _, exists := cfg.Templates[newName]; exists {
		return fmt.Errorf("template %q already exists", newName)
	}

	cfg.Templates[newName] = tmpl
	delete(cfg.Templates, oldName)

	for name, task := range cfg.Tasks {
		if task.Template == oldName {
			task.Template = newName
			cfg.Tasks[name] = task
		}
	}

	// Permission allowlists reference templates by name too, and Validate()
	// requires their literal entries to resolve. Skipping this would make a
	// rename fail against a name that no longer exists, reporting a template
	// the operator never touched. Runs after the re-key above so a template
	// referencing itself follows its own rename.
	for name, tmpl := range cfg.Templates {
		spawn := renameEntries(tmpl.Permissions.CanSpawn, oldName, newName)
		consult := renameEntries(tmpl.Permissions.CanConsult, oldName, newName)
		if spawn == nil && consult == nil {
			continue
		}
		if spawn != nil {
			tmpl.Permissions.CanSpawn = spawn
		}
		if consult != nil {
			tmpl.Permissions.CanConsult = consult
		}
		cfg.Templates[name] = tmpl
	}
	if cfg.Delegation != nil {
		for profileName, profile := range cfg.Delegation.Profiles {
			for role, target := range profile.Roles {
				if target.Template == oldName {
					target.Template = newName
					profile.Roles[role] = target
				}
			}
			cfg.Delegation.Profiles[profileName] = profile
		}
	}
	return nil
}

// RenameRole re-keys a declared delegation role and every profile mapping.
func RenameRole(cfg *Config, oldName, newName string) error {
	if cfg.Delegation == nil {
		return fmt.Errorf("delegation is not configured")
	}
	if newName == "" {
		return fmt.Errorf("new role name must not be empty")
	}
	if cfg.Delegation.HasRole(newName) {
		return fmt.Errorf("role %q already exists", newName)
	}
	found := false
	if role, ok := cfg.Delegation.Roles[oldName]; ok {
		cfg.Delegation.Roles[newName] = role
		delete(cfg.Delegation.Roles, oldName)
		found = true
	}
	for profileName, profile := range cfg.Delegation.Profiles {
		if target, ok := profile.Roles[oldName]; ok {
			profile.Roles[newName] = target
			delete(profile.Roles, oldName)
			cfg.Delegation.Profiles[profileName] = profile
			found = true
		}
	}
	if !found {
		return fmt.Errorf("role %q not found", oldName)
	}
	return nil
}

// renameEntries returns a copy of list with every exact oldName replaced by
// newName, or nil when nothing matched. Glob entries are left alone: they name
// a pattern rather than a specific template, so a rename has nothing to
// rewrite in them.
func renameEntries(list []string, oldName, newName string) []string {
	if !slices.Contains(list, oldName) {
		return nil
	}
	out := slices.Clone(list)
	for i, entry := range out {
		if entry == oldName {
			out[i] = newName
		}
	}
	return out
}

// RenameEnvironment returns a copy of cfg with the named environment re-keyed
// from oldName to newName and every reference rewritten (defaults, templates,
// tasks), preserving list order. cfg is not mutated: only the containers that
// change are cloned, the rest is shared with the input.
//
// As with RenameTemplate, name-shape validation is the caller's job; this
// guards the structural invariants: non-empty and distinct new name, old
// exists, new does not collide.
func RenameEnvironment(cfg *Config, oldName, newName string) (*Config, error) {
	if newName == "" {
		return nil, fmt.Errorf("new environment name must not be empty")
	}
	if newName == oldName {
		return nil, fmt.Errorf("new environment name is the same as the current name")
	}
	env, ok := cfg.Environments[oldName]
	if !ok {
		return nil, fmt.Errorf("environment %q not found", oldName)
	}
	if _, exists := cfg.Environments[newName]; exists {
		return nil, fmt.Errorf("environment %q already exists", newName)
	}

	next := *cfg
	next.Environments = maps.Clone(cfg.Environments)
	next.Environments[newName] = env
	delete(next.Environments, oldName)

	next.Defaults.Environments = replaceName(cfg.Defaults.Environments, oldName, newName)

	next.Templates = maps.Clone(cfg.Templates)
	for name, tmpl := range next.Templates {
		tmpl.Environments = replaceName(tmpl.Environments, oldName, newName)
		next.Templates[name] = tmpl
	}
	next.Tasks = maps.Clone(cfg.Tasks)
	for name, task := range next.Tasks {
		task.Environments = replaceName(task.Environments, oldName, newName)
		next.Tasks[name] = task
	}
	return &next, nil
}

// replaceName returns a copy of list with every oldName replaced by newName.
// A list without oldName is returned as-is (shared, never written to).
func replaceName(list []string, oldName, newName string) []string {
	if !slices.Contains(list, oldName) {
		return list
	}
	return renameEntries(list, oldName, newName)
}
