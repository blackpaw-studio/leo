package agent

import (
	"strings"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// AppearanceFacts are the agent facts appearance rules test. Environments is
// the agent's effective list, as the observability wire reports it.
type AppearanceFacts struct {
	Name         string
	Environments []string
	Template     string
	Harness      string
}

// ResolveAppearance resolves an agent's emoji and color independently: the
// manual value wins, else the first valid rule from the top that sets it and
// matches, else it stays unset. Invalid rules are skipped without shifting
// the indices of later ones. Returns nil when neither value is set. The same
// algorithm runs in LeoKit; testdata/appearance-vectors.json keeps the two in
// step.
func ResolveAppearance(rules config.AppearanceRuleList, facts AppearanceFacts, manual *agentstore.Appearance) *observe.Appearance {
	var out observe.Appearance
	if manual != nil {
		if manual.Emoji != "" {
			out.Emoji, out.EmojiSource = manual.Emoji, observe.AppearanceSourceManual
		}
		if manual.Color != "" {
			out.Color, out.ColorSource = manual.Color, observe.AppearanceSourceManual
		}
	}
	for i, rule := range rules {
		if out.Emoji != "" && out.Color != "" {
			break
		}
		if rule.Problem() != "" || !appearanceMatches(rule.When, facts) {
			continue
		}
		if out.Emoji == "" && rule.Emoji != "" {
			out.Emoji, out.EmojiSource = rule.Emoji, observe.AppearanceRuleSource(i)
		}
		if out.Color == "" && rule.Color != "" {
			out.Color, out.ColorSource = rule.Color, observe.AppearanceRuleSource(i)
		}
	}
	if out == (observe.Appearance{}) {
		return nil
	}
	return &out
}

// appearanceMatches reports whether facts satisfy when. Matching is
// case-sensitive. For environments, is and contains test membership, and
// starts_with and glob match when any element does.
func appearanceMatches(when config.AppearanceCondition, facts AppearanceFacts) bool {
	switch when.Field {
	case config.AppearanceFieldEnvironments:
		for _, env := range facts.Environments {
			if matchAppearanceValue(when.Op, env, when.Value, true) {
				return true
			}
		}
		return false
	case config.AppearanceFieldName:
		return matchAppearanceValue(when.Op, facts.Name, when.Value, false)
	case config.AppearanceFieldTemplate:
		return matchAppearanceValue(when.Op, facts.Template, when.Value, false)
	case config.AppearanceFieldHarness:
		return matchAppearanceValue(when.Op, normalizeHarness(facts.Harness), when.Value, false)
	}
	return false
}

// matchAppearanceValue applies op to one string. A list element (isElement)
// reads contains as equality, so contains on a list is membership.
func matchAppearanceValue(op, s, value string, isElement bool) bool {
	switch op {
	case config.AppearanceOpIs:
		return s == value
	case config.AppearanceOpContains:
		if isElement {
			return s == value
		}
		return strings.Contains(s, value)
	case config.AppearanceOpStartsWith:
		return strings.HasPrefix(s, value)
	case config.AppearanceOpGlob:
		return globMatch(value, s)
	}
	return false
}

// globMatch reports whether s matches pattern in full, where * matches any
// run of code points (slashes included) and ? exactly one. Every other
// character matches itself; config validation rejects [, ] and \.
func globMatch(pattern, s string) bool {
	p, t := []rune(pattern), []rune(s)
	pi, ti := 0, 0
	star, mark := -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case star >= 0:
			mark++
			pi, ti = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// AgentAppearance resolves the appearance of the agent filed under name from
// the same facts the observability wire reports for it: its effective
// environments, its template, and that template's harness. A nil cfg leaves
// only the manual values.
func AgentAppearance(cfg *config.Config, name, template string, override []string, manual *agentstore.Appearance) *observe.Appearance {
	envs, _, _ := EnvironmentsView(cfg, name, template, override)
	facts := AppearanceFacts{Name: name, Environments: envs, Template: template}
	var rules config.AppearanceRuleList
	if cfg != nil {
		rules = cfg.AppearanceRules
		if tmpl, ok := cfg.Templates[template]; ok && template != "" {
			facts.Harness = cfg.TemplateHarness(tmpl)
		}
	}
	return ResolveAppearance(rules, facts, manual)
}
