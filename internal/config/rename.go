package config

import "fmt"

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
	return nil
}
