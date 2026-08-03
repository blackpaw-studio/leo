package agent

import (
	"slices"
	"sort"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// StaleAgent describes one running agent whose wiring would change if it were
// restarted — i.e. an agent still running args or env that today's binary and
// config no longer produce.
//
// Env drift carries KEY NAMES ONLY, never values. A record's Env holds live
// credentials (see the Record doc in manager.go, which omits env from the API
// payload for the same reason), and this struct is served over the daemon's
// IPC API and printed to a terminal.
type StaleAgent struct {
	Name string `json:"name"`
	// ArgsBefore/ArgsAfter are the stored and re-resolved argv with session
	// tokens stripped; both empty when only env drifted.
	ArgsBefore []string `json:"args_before,omitempty"`
	ArgsAfter  []string `json:"args_after,omitempty"`
	EnvAdded   []string `json:"env_added,omitempty"`
	EnvChanged []string `json:"env_changed,omitempty"`
	EnvRemoved []string `json:"env_removed,omitempty"`
}

// StaleAgents reports which running agents would actually change if restarted.
//
// Staleness is defined as "a restart would produce different wiring" rather
// than by stamping a version at spawn: resolveRestartArgs is the same pure
// function Restart itself uses, so dry-running it and diffing against the
// stored record catches binary upgrades and leo.yaml/template edits alike,
// with no false positives. Records restart declines to re-resolve (no
// template, deleted template, changed harness) fall back to their stored args
// by design and are never reported — offering a restart there would be a lie.
//
// Returns entries sorted by name so the update prompt is stable across runs.
func (m *Manager) StaleAgents() []StaleAgent {
	cfg, err := m.cfgLoader()
	if err != nil {
		return nil
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return nil
	}

	running := m.sup.EphemeralAgents()
	out := make([]StaleAgent, 0, len(running))
	for name := range running {
		rec, ok := stored[name]
		if !ok {
			continue
		}
		newArgs, newEnv := resolveRestartArgs(cfg, rec, m.webToken)
		// resolveRestartArgs returns the record's own args verbatim when it
		// can't re-resolve. Comparing identical slices reports no drift, so
		// those records fall out here without a special case.
		if drift, changed := diffWiring(rec, newArgs, newEnv); changed {
			drift.Name = name
			out = append(out, drift)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// diffWiring compares a record's stored wiring against freshly resolved args
// and env, reporting what a restart would change. changed is false when the
// two are equivalent.
func diffWiring(rec agentstore.Record, newArgs []string, newEnv map[string]string) (StaleAgent, bool) {
	var drift StaleAgent

	before := stripSessionArgs(rec.ClaudeArgs)
	after := stripSessionArgs(newArgs)
	if !slices.Equal(before, after) {
		drift.ArgsBefore, drift.ArgsAfter = before, after
	}

	for k := range newEnv {
		old, had := rec.Env[k]
		switch {
		case !had:
			drift.EnvAdded = append(drift.EnvAdded, k)
		case old != newEnv[k]:
			drift.EnvChanged = append(drift.EnvChanged, k)
		}
	}
	for k := range rec.Env {
		if _, still := newEnv[k]; !still {
			drift.EnvRemoved = append(drift.EnvRemoved, k)
		}
	}
	sort.Strings(drift.EnvAdded)
	sort.Strings(drift.EnvChanged)
	sort.Strings(drift.EnvRemoved)

	changed := drift.ArgsBefore != nil || len(drift.EnvAdded) > 0 ||
		len(drift.EnvChanged) > 0 || len(drift.EnvRemoved) > 0
	return drift, changed
}

// sessionArgFlags carry a session token that differs by construction between a
// stored record and a fresh resolve (resolveRestartArgs deliberately omits
// --session-id; Restart's caller applies --resume afterwards). Comparing them
// would report every agent as stale forever.
var sessionArgFlags = map[string]bool{"--session-id": true, "--resume": true, "-s": true}

// stripSessionArgs drops session flags and their values from argv.
func stripSessionArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if sessionArgFlags[args[i]] {
			i++ // also skip the token that follows
			continue
		}
		out = append(out, args[i])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
