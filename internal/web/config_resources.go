package web

import (
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// Restart classes of a resource: what a saved change needs before running
// agents or the daemon see it.
const (
	restartNone    = "none"
	restartAgents  = "agents"
	restartService = "service"
)

// Actions a collection resource offers beyond editing its items.
const (
	actionAdd       = "add"
	actionRename    = "rename"
	actionDuplicate = "duplicate"
	actionDelete    = "delete"
)

// configSub is a nested struct edited with its parent (template permissions
// and push, defaults push): its own schema section, nested under Key in JSON.
type configSub struct {
	Key     string
	Section schema.Section
	// of returns a pointer to the nested struct inside the parent target.
	of func(target any) any
}

// configResource is one thing the web UI edits and the JSON API exposes: the
// schema sections it renders, how to find it inside a loaded config, and what
// a save needs afterwards. Both the HTML forms and the JSON routes drive the
// same descriptor, so they cannot disagree about it.
type configResource struct {
	Key        string
	Label      string
	Noun       string // "Template", for messages
	Collection bool
	Actions    []string
	Section    schema.Section
	Subs       []configSub
	// HarnessOptions marks resources whose scope carries harness/harness_options.
	HarnessOptions bool
	// Cascades marks resources whose empty fields inherit from defaults.
	Cascades bool
	Restart  string

	// locate returns a pointer to the struct to edit: the live field for a
	// singleton, a copy of the map entry for a collection (written back by put).
	locate func(cfg *config.Config, name string) (any, bool)
	put    func(cfg *config.Config, name string, v any)
	names  func(cfg *config.Config) []string
	// setHarnessOptions stores parsed options on target (HarnessOptions only).
	setHarnessOptions func(target any, opts map[string]any)
	// add creates an empty item; the item must not exist yet.
	add    func(cfg *config.Config, name string, extra map[string]string) *ConfigError
	remove func(cfg *config.Config, name string) bool
	saved  func(name string) string
}

func (r *configResource) flag(c *ConfigService) *atomic.Bool {
	switch r.Restart {
	case restartAgents:
		return &c.agentsRestartNeeded
	case restartService:
		return &c.serviceRestartNeeded
	}
	return nil
}

func (r *configResource) has(action string) bool {
	for _, a := range r.Actions {
		if a == action {
			return true
		}
	}
	return false
}

func sortedNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// taken is the 422 for an add/rename/duplicate onto an existing name.
func taken(field, noun, name string) *ConfigError {
	return errInvalid(field, fmt.Sprintf("%s %q already exists", noun, name))
}

var configResources = []*configResource{
	{
		Key: "defaults", Label: "Defaults", Noun: "Defaults",
		Section: schema.SectionDefaults, HarnessOptions: true, Restart: restartAgents,
		Subs: []configSub{{Key: "push", Section: schema.SectionPushDefaults,
			of: func(t any) any { return &t.(*config.DefaultsConfig).Push }}},
		locate: func(cfg *config.Config, _ string) (any, bool) { return &cfg.Defaults, true },
		put:    func(*config.Config, string, any) {}, // &cfg.Defaults is the live field
		setHarnessOptions: func(t any, o map[string]any) {
			t.(*config.DefaultsConfig).HarnessOptions = o
		},
		saved: func(string) string { return "Defaults saved" },
	},
	{
		Key: "templates", Label: "Templates", Noun: "Template", Collection: true,
		Actions: []string{actionAdd, actionRename, actionDuplicate, actionDelete},
		Section: schema.SectionTemplate, HarnessOptions: true, Cascades: true, Restart: restartAgents,
		Subs: []configSub{
			{Key: "permissions", Section: schema.SectionPermissions, of: func(t any) any { return &t.(*config.TemplateConfig).Permissions }},
			{Key: "push", Section: schema.SectionPushTemplate, of: func(t any) any { return &t.(*config.TemplateConfig).Push }},
		},
		locate: func(cfg *config.Config, name string) (any, bool) {
			t, ok := cfg.Templates[name]
			return &t, ok
		},
		put:   func(cfg *config.Config, name string, v any) { cfg.Templates[name] = *(v.(*config.TemplateConfig)) },
		names: func(cfg *config.Config) []string { return sortedNames(cfg.Templates) },
		setHarnessOptions: func(t any, o map[string]any) {
			t.(*config.TemplateConfig).HarnessOptions = o
		},
		add: func(cfg *config.Config, name string, _ map[string]string) *ConfigError {
			if cfg.Templates == nil {
				cfg.Templates = make(map[string]config.TemplateConfig)
			}
			if _, exists := cfg.Templates[name]; exists {
				return taken("name", "Template", name)
			}
			cfg.Templates[name] = config.TemplateConfig{}
			return nil
		},
		remove: func(cfg *config.Config, name string) bool {
			_, ok := cfg.Templates[name]
			delete(cfg.Templates, name)
			return ok
		},
		saved: func(name string) string { return fmt.Sprintf("Template %q saved", name) },
	},
	{
		Key: "tasks", Label: "Tasks", Noun: "Task", Collection: true,
		Actions: []string{actionAdd, actionDelete},
		Section: schema.SectionTask, HarnessOptions: true, Cascades: true, Restart: restartNone,
		locate: func(cfg *config.Config, name string) (any, bool) {
			t, ok := cfg.Tasks[name]
			return &t, ok
		},
		put:   func(cfg *config.Config, name string, v any) { cfg.Tasks[name] = *(v.(*config.TaskConfig)) },
		names: func(cfg *config.Config) []string { return sortedNames(cfg.Tasks) },
		setHarnessOptions: func(t any, o map[string]any) {
			t.(*config.TaskConfig).HarnessOptions = o
		},
		// A bare task: name + schedule. The prompt file is auto-named and starts
		// out non-existent, and the task starts disabled so an incomplete config
		// never fires on a cron tick.
		add: func(cfg *config.Config, name string, extra map[string]string) *ConfigError {
			if cfg.Tasks == nil {
				cfg.Tasks = make(map[string]config.TaskConfig)
			}
			if _, exists := cfg.Tasks[name]; exists {
				return taken("name", "Task", name)
			}
			cfg.Tasks[name] = config.TaskConfig{Schedule: extra["schedule"], PromptFile: "prompts/" + name + ".md"}
			return nil
		},
		remove: func(cfg *config.Config, name string) bool {
			_, ok := cfg.Tasks[name]
			delete(cfg.Tasks, name)
			return ok
		},
		saved: func(name string) string { return fmt.Sprintf("Task %q saved", name) },
	},
	{
		Key: "environments", Label: "Environments", Noun: "Environment", Collection: true,
		Actions: []string{actionAdd, actionRename, actionDelete},
		Section: schema.SectionEnvironment, Restart: restartAgents,
		locate: func(cfg *config.Config, name string) (any, bool) {
			env, ok := cfg.Environments[name]
			return &schema.EnvironmentEntry{Env: env}, ok
		},
		put: func(cfg *config.Config, name string, v any) {
			cfg.Environments[name] = v.(*schema.EnvironmentEntry).Env
		},
		names: func(cfg *config.Config) []string { return sortedNames(cfg.Environments) },
		add: func(cfg *config.Config, name string, _ map[string]string) *ConfigError {
			if _, exists := cfg.Environments[name]; exists {
				return taken("name", "Environment", name)
			}
			if cfg.Environments == nil {
				cfg.Environments = make(map[string]map[string]string)
			}
			cfg.Environments[name] = map[string]string{}
			return nil
		},
		remove: func(cfg *config.Config, name string) bool {
			_, ok := cfg.Environments[name]
			delete(cfg.Environments, name)
			return ok
		},
		saved: func(name string) string { return fmt.Sprintf("Environment %q saved", name) },
	},
	{
		Key: "web", Label: "Web UI", Noun: "Web UI",
		Section: schema.SectionWeb, Restart: restartService,
		locate: func(cfg *config.Config, _ string) (any, bool) { return &cfg.Web, true },
		put:    func(*config.Config, string, any) {},
		saved:  func(string) string { return "Web UI settings saved" },
	},
	{
		Key: "client", Label: "Remote client", Noun: "Remote client",
		Section: schema.SectionClient, Restart: restartNone,
		locate: func(cfg *config.Config, _ string) (any, bool) { return &cfg.Client, true },
		put:    func(*config.Config, string, any) {},
		saved:  func(string) string { return "Remote client settings saved" },
	},
	{
		Key: "hosts", Label: "Remote hosts", Noun: "Host", Collection: true,
		Actions: []string{actionAdd, actionDelete},
		Section: schema.SectionClientHost, Restart: restartNone,
		locate: func(cfg *config.Config, name string) (any, bool) {
			h, ok := cfg.Client.Hosts[name]
			return &h, ok
		},
		put:   func(cfg *config.Config, name string, v any) { cfg.Client.Hosts[name] = *(v.(*config.HostConfig)) },
		names: func(cfg *config.Config) []string { return sortedNames(cfg.Client.Hosts) },
		add: func(cfg *config.Config, name string, _ map[string]string) *ConfigError {
			if cfg.Client.Hosts == nil {
				cfg.Client.Hosts = make(map[string]config.HostConfig)
			}
			if _, exists := cfg.Client.Hosts[name]; exists {
				return taken("name", "Host", name)
			}
			cfg.Client.Hosts[name] = config.HostConfig{}
			return nil
		},
		remove: func(cfg *config.Config, name string) bool {
			_, ok := cfg.Client.Hosts[name]
			delete(cfg.Client.Hosts, name)
			return ok
		},
		saved: func(name string) string { return fmt.Sprintf("Host %q saved", name) },
	},
}

// resourceByKey finds a resource by its path key, nil when unknown.
func resourceByKey(key string) *configResource {
	for _, r := range configResources {
		if r.Key == key {
			return r
		}
	}
	return nil
}

// fieldKeys is the set of field keys a Validate message may name for
// resource: its registry keys plus "<sub>.<key>" for each subsection.
func fieldKeys(resource string) map[string]bool {
	keys := map[string]bool{}
	if resource == "delegation" {
		keys["template"], keys["model"], keys["effort"] = true, true, true
		return keys
	}
	r := resourceByKey(resource)
	if r == nil {
		return keys
	}
	for _, f := range schema.FieldsFor(r.Section) {
		keys[f.Key] = true
	}
	for _, sub := range r.Subs {
		keys[sub.Key] = true
		for _, f := range schema.FieldsFor(sub.Section) {
			keys[sub.Key+"."+f.Key] = true
		}
	}
	return keys
}
