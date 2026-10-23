package web

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"sync/atomic"

	"gopkg.in/yaml.v3"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// WriteResult is what a successful write reports.
type WriteResult struct {
	// Revision is leo.yaml's revision after the write (unchanged for a no-op).
	Revision string
	// Restart reads the shared restart flags after the write.
	Restart RestartRequired
	// Warning is set when the config was saved but the reload failed.
	Warning string
}

// errNoWrite is returned by a Mutate apply func that found nothing to change:
// the call succeeds without touching leo.yaml.
var errNoWrite = errors.New("nothing to write")

// mutateOpts parameterizes Mutate.
type mutateOpts struct {
	// IfMatch is the revision the caller read; "" means unchecked (the HTML
	// forms). A mismatch writes nothing.
	IfMatch string
	// Scope maps Validate messages to the edited resource's fields.
	Scope validationScope
	// Flag, when non-nil, is raised after a successful save.
	Flag *atomic.Bool
	// AfterSave runs under the lock once leo.yaml is written, before the reload.
	AfterSave func(*config.Config)
}

// Mutate is the single write path: under the config write lock it loads
// leo.yaml with its revision, compares IfMatch, applies, validates, saves,
// runs AfterSave, reloads and raises the restart flag. An error means
// nothing was written. Every HTML handler and every JSON route mutates
// through it.
func (c *ConfigService) Mutate(opts mutateOpts, apply func(*config.Config) error) (WriteResult, error) {
	defer c.lockConfigWrite()()
	cfg, rev, err := config.LoadWithRevision(c.configPath)
	if err != nil {
		return WriteResult{}, configUnavailable(err)
	}
	if opts.IfMatch != "" && opts.IfMatch != rev {
		return WriteResult{}, errConflict(rev)
	}
	if err := apply(cfg); err != nil {
		if errors.Is(err, errNoWrite) {
			return WriteResult{Revision: rev, Restart: c.RestartFlags()}, nil
		}
		return WriteResult{}, asConfigError(err)
	}
	if err := cfg.Validate(); err != nil {
		var verrs *config.ValidationErrors
		if errors.As(err, &verrs) {
			fe := errInvalidFields(fieldProblems(opts.Scope, verrs.Messages, fieldKeys))
			fe.Message = err.Error()
			return WriteResult{}, fe
		}
		return WriteResult{}, errInvalid("", err.Error())
	}
	if err := config.Save(c.configPath, cfg); err != nil {
		return WriteResult{}, errInvalid("", fmt.Sprintf("Failed to save: %v", err))
	}
	if opts.AfterSave != nil {
		opts.AfterSave(cfg)
	}
	warn := c.reloadConfigOrWarn()
	if opts.Flag != nil {
		opts.Flag.Store(true)
	}
	return WriteResult{Revision: c.currentRevision(rev), Restart: c.RestartFlags(), Warning: warn}, nil
}

// currentRevision re-reads leo.yaml's revision after a save, falling back to
// the pre-save one only if the read fails.
func (c *ConfigService) currentRevision(fallback string) string {
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return fallback
	}
	return config.RevisionOf(data)
}

// asConfigError passes a *ConfigError through and wraps anything else as a
// form-level 422, the status Validate-class failures carry.
func asConfigError(err error) *ConfigError {
	var ce *ConfigError
	if errors.As(err, &ce) {
		return ce
	}
	return errInvalid("", err.Error())
}

// mutateConfig is Mutate for HTML handlers that only need the flash outcome:
// unchecked, no flag. A non-empty errMsg means nothing was written; warn
// reports a failed reload.
func (c *ConfigService) mutateConfig(apply func(*config.Config) error) (warn, errMsg string) {
	res, err := c.Mutate(mutateOpts{}, apply)
	if err != nil {
		return "", err.Error()
	}
	return res.Warning, ""
}

// ApplyValues saves a section form into one item: the single save path for
// every schema-driven editor. form is what schema.Apply parses (a browser's
// POST, or the JSON overlay's rendering); check, when non-nil, runs after the
// flat fields are applied and may reject the write with field problems.
func (c *ConfigService) ApplyValues(ifMatch string, res *configResource, name string, form url.Values,
	check func(cfg *config.Config, target any, form url.Values) []schema.Problem) (WriteResult, error) {
	opts := mutateOpts{IfMatch: ifMatch, Scope: validationScope{Resource: res.Key, Name: name}, Flag: res.flag(c)}
	return c.Mutate(opts, func(cfg *config.Config) error {
		target, ok := res.locate(cfg, name)
		if !ok {
			return errNotFound(messageNotFoundFallback)
		}
		if err := applyForm(cfg, res, target, form, check); err != nil {
			return err
		}
		res.put(cfg, name, target)
		return nil
	})
}

// applyForm parses form into target: flat fields first, then (after a harness
// change in the same form is already reflected) harness_options, then the
// nested subsections.
func applyForm(cfg *config.Config, res *configResource, target any, form url.Values,
	check func(*config.Config, any, url.Values) []schema.Problem) error {
	if err := schema.Apply(target, res.Section, form); err != nil {
		return fieldError(err, "")
	}
	if check != nil {
		if problems := check(cfg, target, form); len(problems) > 0 {
			return errInvalidFields(problems)
		}
	}
	if res.HarnessOptions {
		_, harnessName, _ := harnessView(target, cfg)
		opts, err := applyScopeHarnessOptions(form, harnessName)
		if err != nil {
			return fieldError(err, "")
		}
		res.setHarnessOptions(target, opts)
	}
	for _, sub := range res.Subs {
		if err := schema.Apply(sub.of(target), sub.Section, form); err != nil {
			return fieldError(err, sub.Key+".")
		}
	}
	return nil
}

// fieldError turns a parse error into a 422, pinned to its field when the
// error carries one (prefixed for nested sections).
func fieldError(err error, prefix string) *ConfigError {
	var fe *schema.FieldError
	if errors.As(err, &fe) {
		out := errInvalid(prefix+fe.Key, err.Error())
		return out
	}
	return errInvalid("", err.Error())
}

// AddItem creates an empty item named name. extra carries the resource's
// required extras (a task's schedule).
func (c *ConfigService) AddItem(ifMatch string, res *configResource, name string, extra map[string]string) (WriteResult, error) {
	if !validEntityName(name) {
		return WriteResult{}, errInvalid("name", entityNameError)
	}
	if res.Key == "tasks" && extra["schedule"] == "" {
		return WriteResult{}, errInvalid("schedule", "Schedule is required")
	}
	return c.Mutate(mutateOpts{IfMatch: ifMatch, Scope: validationScope{Resource: res.Key, Name: name}}, func(cfg *config.Config) error {
		if ce := res.add(cfg, name, extra); ce != nil {
			return ce
		}
		return nil
	})
}

// DeleteItem removes an item. Validate rejects the save while something
// still references it, so the error lists the reference rather than leaving a
// dangling name behind.
func (c *ConfigService) DeleteItem(ifMatch string, res *configResource, name string) (WriteResult, error) {
	return c.Mutate(mutateOpts{IfMatch: ifMatch}, func(cfg *config.Config) error {
		if !res.remove(cfg, name) {
			return errNotFound("%s %q not found", res.Noun, name)
		}
		return nil
	})
}

// RenameItem re-keys a template or environment and rewrites every reference,
// including persisted agent records. Renaming to the same name writes nothing.
func (c *ConfigService) RenameItem(ifMatch string, res *configResource, name, newName string) (WriteResult, error) {
	if newName == "" {
		return WriteResult{}, errInvalid("new_name", "New name is required")
	}
	if !validEntityName(newName) {
		return WriteResult{}, errInvalid("new_name", entityNameError)
	}
	opts := mutateOpts{IfMatch: ifMatch}
	var apply func(*config.Config) error
	switch res.Key {
	case "templates":
		opts.AfterSave = func(cfg *config.Config) { renameTemplateInAgentRecords(cfg.HomePath, name, newName) }
		apply = func(cfg *config.Config) error {
			if _, ok := cfg.Templates[name]; !ok {
				return errNotFound("Template %q not found", name)
			}
			if name == newName {
				return errNoWrite
			}
			if err := config.RenameTemplate(cfg, name, newName); err != nil {
				return errInvalid("new_name", err.Error())
			}
			return nil
		}
	case "environments":
		opts.AfterSave = func(cfg *config.Config) { renameInAgentRecords(cfg.HomePath, name, newName) }
		apply = func(cfg *config.Config) error {
			if _, ok := cfg.Environments[name]; !ok {
				return errNotFound("Environment %q not found", name)
			}
			renamed, err := config.RenameEnvironment(cfg, name, newName)
			if err != nil {
				return errInvalid("new_name", err.Error())
			}
			*cfg = *renamed
			return nil
		}
	default:
		return WriteResult{}, errBadRequest("%s cannot be renamed", res.Label)
	}
	return c.Mutate(opts, apply)
}

// DuplicateItem copies a template under a new name. The copy is deep, so
// harness_options, permissions, push and keys the schema form does not edit
// all survive.
func (c *ConfigService) DuplicateItem(ifMatch string, res *configResource, name, newName string) (WriteResult, error) {
	if res.Key != "templates" {
		return WriteResult{}, errBadRequest("%s cannot be duplicated", res.Label)
	}
	if !validEntityName(newName) {
		return WriteResult{}, errInvalid("new_name", entityNameError)
	}
	return c.Mutate(mutateOpts{IfMatch: ifMatch}, func(cfg *config.Config) error {
		src, ok := cfg.Templates[name]
		if !ok {
			return errNotFound("Template %q not found", name)
		}
		if _, exists := cfg.Templates[newName]; exists {
			return taken("new_name", "Template", newName)
		}
		raw, err := yaml.Marshal(src)
		if err != nil {
			return fmt.Errorf("copying template: %w", err)
		}
		var cp config.TemplateConfig
		if err := yaml.Unmarshal(raw, &cp); err != nil {
			return fmt.Errorf("copying template: %w", err)
		}
		cfg.Templates[newName] = cp
		return nil
	})
}

// renameTemplateInAgentRecords moves the template pointer on every persisted
// agent record spawned from the old name. Best-effort: a failure must not fail
// a rename that already updated config and tasks, so it logs and continues.
func renameTemplateInAgentRecords(homePath, oldName, newName string) {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		return
	}
	for recName, rec := range records {
		if rec.Template != oldName {
			continue
		}
		if err := agentstore.Update(homePath, recName, func(r agentstore.Record) agentstore.Record {
			r.Template = newName
			return r
		}); err != nil {
			// #nosec G706 -- names are validated identifiers (validEntityName / existing config + agentstore keys); no control chars can reach the log
			log.Printf("template rename %q→%q: agentstore.Update(%q) failed: %v", oldName, newName, recName, err)
		}
	}
}
