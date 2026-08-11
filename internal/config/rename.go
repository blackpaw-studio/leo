package config

import (
	"fmt"
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
