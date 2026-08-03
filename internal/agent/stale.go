package agent

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// StaleAgent describes one running agent whose wiring would change if it were
// restarted — i.e. an agent still running args or env that today's binary and
// config no longer produce.
//
// Nothing here carries a free-form value. This struct is served over the
// daemon's IPC API and printed to a terminal, while the wiring it describes
// holds live credentials (env) and free-form text (argv: an agent's whole
// --append-system-prompt lands in there). So env drift is KEY NAMES ONLY —
// the same reason Record omits env entirely, see manager.go — and argv drift
// is summarized per flag with long/multi-line values elided rather than
// echoed.
type StaleAgent struct {
	Name string `json:"name"`
	// ArgsChanged describes the argv delta one entry per flag, already
	// redacted: "--model sonnet -> opus", "--append-system-prompt changed",
	// "+--remote-control". Empty when only env drifted.
	ArgsChanged []string `json:"args_changed,omitempty"`
	EnvAdded    []string `json:"env_added,omitempty"`
	EnvChanged  []string `json:"env_changed,omitempty"`
	EnvRemoved  []string `json:"env_removed,omitempty"`
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
		drift.ArgsChanged = summarizeArgsDelta(before, after)
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

	changed := len(drift.ArgsChanged) > 0 || len(drift.EnvAdded) > 0 ||
		len(drift.EnvChanged) > 0 || len(drift.EnvRemoved) > 0
	return drift, changed
}

// maxReportedArgValue is the longest argv value echoed into a drift report.
// Anything longer is elided as "changed": a value that big is free-form text
// (--append-system-prompt carries an entire system prompt), not an identifier
// an operator scans for.
const maxReportedArgValue = 40

// summarizeArgsDelta renders an argv delta one entry per flag, redacted.
//
// Values are echoed only when short and single-line — enough for the
// "--model sonnet -> opus" case that makes the report useful — and otherwise
// reduced to the flag name plus what happened to it. Positional (non-flag)
// tokens are counted, never printed, since leo passes an agent's opening
// prompt positionally.
func summarizeArgsDelta(before, after []string) []string {
	bFlags, bPositional := parseArgs(before)
	aFlags, aPositional := parseArgs(after)

	seen := make(map[string]bool, len(bFlags)+len(aFlags))
	flags := make([]string, 0, len(bFlags)+len(aFlags))
	for _, m := range []map[string]string{bFlags, aFlags} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				flags = append(flags, k)
			}
		}
	}
	sort.Strings(flags)

	out := make([]string, 0, len(flags))
	for _, f := range flags {
		oldVal, hadOld := bFlags[f]
		newVal, hasNew := aFlags[f]
		switch {
		case hadOld && !hasNew:
			out = append(out, "-"+f)
		case !hadOld && hasNew:
			out = append(out, "+"+f+redactedSuffix(newVal))
		case oldVal != newVal:
			out = append(out, f+" "+redactedTransition(oldVal, newVal))
		}
	}
	if bPositional != aPositional {
		out = append(out, fmt.Sprintf("positional args %d -> %d", bPositional, aPositional))
	}
	return out
}

// parseArgs splits argv into flag→value (empty value for a bare flag) and a
// count of positional tokens. A token following a flag is its value only when
// it doesn't itself look like a flag.
//
// A repeated flag (--add-dir, say) keeps only its last value here. That costs
// nothing: drift is DETECTED by comparing full argv slices before this runs —
// this only shapes how the difference is described.
func parseArgs(args []string) (map[string]string, int) {
	flags := make(map[string]string, len(args))
	positional := 0
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if !strings.HasPrefix(flag, "-") {
			positional++
			continue
		}
		val := ""
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			val = args[i+1]
			i++
		}
		flags[flag] = val
	}
	return flags, positional
}

func redactedSuffix(val string) string {
	if val == "" {
		return ""
	}
	if reportableArgValue(val) {
		return " " + val
	}
	return " (set)"
}

func redactedTransition(oldVal, newVal string) string {
	if reportableArgValue(oldVal) && reportableArgValue(newVal) {
		return oldVal + " -> " + newVal
	}
	return "changed"
}

// reportableArgValue reports whether an argv value is short and single-line
// enough to echo verbatim.
func reportableArgValue(v string) bool {
	return len(v) <= maxReportedArgValue && !strings.ContainsAny(v, "\n\r")
}

// sessionArgFlags carry a session token that differs by construction between a
// stored record and a fresh resolve (resolveRestartArgs deliberately omits
// --session-id; Restart's caller applies --resume afterwards). Comparing them
// would report every agent as stale forever.
//
// opencode's -s is defensive rather than load-bearing: its session args are
// injected per-launch by the tmuxtui driver's RefreshSessionArgs and never
// persisted to the record, so today no stored argv contains it. Listed anyway
// so a future change that does persist it can't silently reintroduce the
// report-forever bug.
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
