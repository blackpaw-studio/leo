package schema

import (
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// Option is one <option> in a KindSelect control.
type Option struct{ Value, Label string }

// modelOptions mirrors the authoritative model list validated by
// Config.Validate() (internal/config/config.go), plus a leading empty
// "inherit" option. Keep in sync if that list changes.
var modelOptions = []Option{
	{"", "inherit"},
	{"sonnet", "sonnet"},
	{"opus", "opus"},
	{"haiku", "haiku"},
	{"sonnet[1m]", "sonnet[1m]"},
	{"opus[1m]", "opus[1m]"},
}

// OptionSources resolves named option lists against a loaded config. Agents
// is injected because listing claude sub-agents shells out (see web.Server).
type OptionSources struct {
	Cfg    *config.Config
	Agents func() []string
}

// For returns the options for a registry Options name. Unknown names panic:
// a typo in the registry should fail loudly in tests, not render empty.
func (o OptionSources) For(name string) []Option {
	opts := o.TryFor(name)
	if opts == nil {
		panic("schema: unknown options source " + name)
	}
	return opts
}

// TryFor resolves a named source like For but returns nil for unknown
// names. Harness OptionField.Source values are loose by-name hints, so
// unresolvable ones must fall back to a plain control, not panic.
func (o OptionSources) TryFor(name string) []Option {
	switch name {
	case "models":
		return modelOptions
	case "harnesses":
		return namedKeys(harness.Names(), "inherit")
	case "permission_modes":
		return []Option{{"", "inherit"}, {"default", "default"}, {"acceptEdits", "acceptEdits"},
			{"auto", "auto"}, {"bypassPermissions", "bypassPermissions"},
			{"dontAsk", "dontAsk"}, {"plan", "plan"}}
	case "runtimes":
		return []Option{{"oneshot", "oneshot"}, {"persistent", "persistent"}}
	case "sessions":
		return namedKeys(keysOf(o.Cfg.Sessions), "derived per task")
	case "templates":
		return namedKeys(keysOf(o.Cfg.Templates), "none")
	case "agents":
		var names []string
		if o.Agents != nil {
			names = o.Agents()
		}
		return namedKeys(names, "none")
	}
	return nil
}

func keysOf[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func namedKeys(keys []string, emptyLabel string) []Option {
	opts := []Option{{"", emptyLabel}}
	for _, k := range keys {
		opts = append(opts, Option{k, k})
	}
	return opts
}
