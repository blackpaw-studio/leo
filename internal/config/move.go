package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// DefaultMoveHookTimeout bounds each move hook when no hook_timeout is set.
const DefaultMoveHookTimeout = 10 * time.Minute

// MoveConfig configures `leo agent move` / `leo agent copy`, globally (the
// top-level `move:` block) or per template (`templates.<t>.move:`). Lists
// concatenate, global first; a template's hook_timeout overrides the global
// one. pre_send and exclude are read on the source host, post_receive on the
// destination. See docs/configuration/agent-move.md.
type MoveConfig struct {
	// Exclude lists rsync exclude patterns for the workspace transfer, e.g.
	// node_modules/ or build output. An excluded path is never sent and
	// never deleted on the destination.
	Exclude []string `yaml:"exclude,omitempty"`
	// HookTimeout bounds each hook (a Go duration); empty means 10m.
	HookTimeout string          `yaml:"hook_timeout,omitempty"`
	Hooks       MoveHooksConfig `yaml:"hooks,omitempty"`
}

// MoveHooksConfig lists executables run around a transfer. Each entry is an
// absolute path (`~/` expanded), exec'd directly with no shell and no args.
type MoveHooksConfig struct {
	// PreSend runs on the source after the agent stops, before transfer.
	PreSend []string `yaml:"pre_send,omitempty"`
	// PostReceive runs on the destination before the agent starts there.
	PostReceive []string `yaml:"post_receive,omitempty"`
}

// ResolvedMove is a template's effective move settings, global then
// template, with hook paths expanded.
type ResolvedMove struct {
	Exclude     []string
	PreSend     []string
	PostReceive []string
	HookTimeout time.Duration
}

// ResolveMove returns the effective move settings for agents of template
// (which may be absent from config: the global block alone then applies).
func (c *Config) ResolveMove(template string) ResolvedMove {
	tmpl := c.Templates[template]
	out := ResolvedMove{
		Exclude:     concat(c.Move.Exclude, tmpl.Move.Exclude),
		PreSend:     expandHooks(concat(c.Move.Hooks.PreSend, tmpl.Move.Hooks.PreSend)),
		PostReceive: expandHooks(concat(c.Move.Hooks.PostReceive, tmpl.Move.Hooks.PostReceive)),
		HookTimeout: DefaultMoveHookTimeout,
	}
	for _, raw := range []string{c.Move.HookTimeout, tmpl.Move.HookTimeout} {
		if d, err := parseHookTimeout(raw); err == nil && raw != "" {
			out.HookTimeout = d
		}
	}
	return out
}

func concat(a, b []string) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	return append(append(make([]string, 0, len(a)+len(b)), a...), b...)
}

func expandHooks(paths []string) []string {
	if paths == nil {
		return nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = expandHome(p)
	}
	return out
}

func parseHookTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return DefaultMoveHookTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return d, nil
}

// validateMove checks the global and every template's move block.
func (c *Config) validateMove() []string {
	errs := validateMoveBlock("move", c.Move)
	for _, name := range slices.Sorted(maps.Keys(c.Templates)) {
		errs = append(errs, validateMoveBlock("templates."+name+".move", c.Templates[name].Move)...)
	}
	return errs
}

func validateMoveBlock(scope string, m MoveConfig) []string {
	var errs []string
	if _, err := parseHookTimeout(m.HookTimeout); err != nil {
		errs = append(errs, fmt.Sprintf("%s.hook_timeout %q: %v", scope, m.HookTimeout, err))
	}
	for _, p := range m.Exclude {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, "\n\r\x00") {
			errs = append(errs, fmt.Sprintf("%s.exclude %q must be a non-empty single-line pattern", scope, p))
		}
	}
	for _, list := range []struct {
		kind  string
		hooks []string
	}{{"pre_send", m.Hooks.PreSend}, {"post_receive", m.Hooks.PostReceive}} {
		for _, h := range list.hooks {
			if msg := hookPathProblem(h); msg != "" {
				errs = append(errs, fmt.Sprintf("%s.hooks.%s %q %s", scope, list.kind, h, msg))
			}
		}
	}
	return errs
}

// hookPathProblem says why h is not a usable hook path, "" when it is.
func hookPathProblem(h string) string {
	switch {
	case strings.TrimSpace(h) == "":
		return "is empty"
	case strings.ContainsAny(h, " \t\n\r\x00"):
		return "must be a bare executable path (hooks take no args and run without a shell)"
	case !filepath.IsAbs(expandHome(h)):
		return "must be absolute (or start with ~/)"
	}
	return ""
}
