package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rivo/uniseg"
	"gopkg.in/yaml.v3"
)

// Agent appearance (docs/configuration/appearance.md): an optional emoji and
// color per agent, set by hand on the agent's record or by the ordered
// appearance_rules here. Colors are palette names; each app maps a name to its
// own light and dark shade.

// appearancePalette is the fixed set of color names, in display order.
var appearancePalette = []string{"red", "orange", "yellow", "green", "teal", "blue", "indigo", "purple", "pink", "gray"}

// AppearancePalette returns the palette's color names, a fresh copy each call.
func AppearancePalette() []string { return slices.Clone(appearancePalette) }

// The agent facts a rule can test.
const (
	AppearanceFieldName         = "name"
	AppearanceFieldEnvironments = "environments"
	AppearanceFieldTemplate     = "template"
	AppearanceFieldHarness      = "harness"
)

// The comparisons a rule can make. For environments, is and contains test
// list membership, and starts_with and glob match when any element does.
const (
	AppearanceOpIs         = "is"
	AppearanceOpContains   = "contains"
	AppearanceOpStartsWith = "starts_with"
	AppearanceOpGlob       = "glob"
)

var (
	appearanceFields = []string{AppearanceFieldName, AppearanceFieldEnvironments, AppearanceFieldTemplate, AppearanceFieldHarness}
	appearanceOps    = []string{AppearanceOpIs, AppearanceOpContains, AppearanceOpStartsWith, AppearanceOpGlob}
)

// MaxAppearanceEmojiBytes caps an emoji's UTF-8 length, so a single
// pathological grapheme cluster (a base with hundreds of combining marks)
// cannot ride along as "one emoji".
const MaxAppearanceEmojiBytes = 64

// AppearanceCondition is a rule's test: Field Op Value, case-sensitive.
type AppearanceCondition struct {
	Field string `yaml:"field" json:"field"`
	Op    string `yaml:"op" json:"op"`
	Value string `yaml:"value" json:"value"`
}

// AppearanceRule sets a color, an emoji, or both on every agent its condition
// matches. A rule that does not decode is kept as its raw YAML (see
// UnmarshalYAML) so a hand-edit mistake never fails Load.
type AppearanceRule struct {
	When  AppearanceCondition `yaml:"when" json:"when"`
	Color string              `yaml:"color,omitempty" json:"color,omitempty"`
	Emoji string              `yaml:"emoji,omitempty" json:"emoji,omitempty"`

	// raw is the rule's YAML exactly as loaded when it did not decode; it is
	// re-marshalled unchanged. malformed is the reason reported for it, and
	// wholeList marks the one entry standing for an appearance_rules value
	// that was not a list at all.
	raw       *yaml.Node
	malformed string
	wholeList bool
}

// appearanceRuleFields is AppearanceRule without its YAML methods.
type appearanceRuleFields struct {
	When  AppearanceCondition `yaml:"when"`
	Color string              `yaml:"color,omitempty"`
	Emoji string              `yaml:"emoji,omitempty"`
}

// UnmarshalYAML decodes a rule leniently: a structurally wrong rule (a string
// where the when map belongs, say) keeps its raw node and is marked
// malformed instead of failing the whole config's Load, which would block
// the daemon's boot or reload. Validate still reports it.
func (r *AppearanceRule) UnmarshalYAML(node *yaml.Node) error {
	var f appearanceRuleFields
	if err := node.Decode(&f); err != nil {
		*r = AppearanceRule{When: f.When, Color: f.Color, Emoji: f.Emoji, raw: node,
			malformed: fmt.Sprintf("rule is malformed (line %d): want when: {field, op, value} plus a color and/or an emoji, all strings", node.Line)}
		return nil
	}
	*r = AppearanceRule{When: f.When, Color: f.Color, Emoji: f.Emoji}
	return nil
}

// MarshalYAML writes a malformed rule back exactly as it was loaded.
func (r AppearanceRule) MarshalYAML() (any, error) {
	if r.raw != nil {
		return r.raw, nil
	}
	return appearanceRuleFields{When: r.When, Color: r.Color, Emoji: r.Emoji}, nil
}

// Problem is why the rule is invalid, or "" when it is valid. An invalid rule
// never matches. The first problem found is the one reported.
func (r AppearanceRule) Problem() string {
	if r.malformed != "" {
		return r.malformed
	}
	switch {
	case !slices.Contains(appearanceFields, r.When.Field):
		return fmt.Sprintf("field %q must be one of %s", r.When.Field, strings.Join(appearanceFields, ", "))
	case !slices.Contains(appearanceOps, r.When.Op):
		return fmt.Sprintf("op %q must be one of %s", r.When.Op, strings.Join(appearanceOps, ", "))
	case r.When.Value == "":
		return "value is required"
	case r.When.Op == AppearanceOpGlob && strings.ContainsAny(r.When.Value, `[]\`):
		return fmt.Sprintf("glob %q may only use the * and ? wildcards", r.When.Value)
	case r.Color == "" && r.Emoji == "":
		return "rule must set a color, an emoji, or both"
	}
	if r.Color != "" {
		if err := ValidateAppearanceColor(r.Color); err != nil {
			return err.Error()
		}
	}
	if r.Emoji != "" {
		if err := ValidateAppearanceEmoji(r.Emoji); err != nil {
			return err.Error()
		}
	}
	return ""
}

// AppearanceRuleList is the ordered appearance_rules list. Like a rule, the
// list itself decodes leniently: a value that is not a list loads as one
// malformed entry and is written back unchanged.
type AppearanceRuleList []AppearanceRule

// UnmarshalYAML decodes each rule leniently (see AppearanceRule).
func (l *AppearanceRuleList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	switch {
	case node.Kind == yaml.ScalarNode && node.Tag == "!!null":
		*l = nil
		return nil
	case node.Kind != yaml.SequenceNode:
		*l = AppearanceRuleList{{raw: node, wholeList: true, malformed: fmt.Sprintf("appearance_rules must be a list of rules (line %d)", node.Line)}}
		return nil
	}
	rules := make(AppearanceRuleList, 0, len(node.Content))
	for _, item := range node.Content {
		var r AppearanceRule
		if err := item.Decode(&r); err != nil {
			return err
		}
		rules = append(rules, r)
	}
	*l = rules
	return nil
}

// MarshalYAML writes a list that did not load as a list back unchanged.
func (l AppearanceRuleList) MarshalYAML() (any, error) {
	if len(l) == 1 && l[0].wholeList {
		return l[0].raw, nil
	}
	return []AppearanceRule(l), nil
}

// AppearanceRuleError is one invalid rule: its 0-based index and why.
type AppearanceRuleError struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// Errors lists every invalid rule in order; empty (never nil) when all are
// valid.
func (l AppearanceRuleList) Errors() []AppearanceRuleError {
	out := []AppearanceRuleError{}
	for i, r := range l {
		if p := r.Problem(); p != "" {
			out = append(out, AppearanceRuleError{Index: i, Reason: p})
		}
	}
	return out
}

// AppearanceRuleWarnings reports every invalid rule as
// "appearance_rules[i]: reason". It is a warning, not a Validate error: a bad
// rule is only skipped when resolving, so it must not stop `leo service
// start`, `leo run` or a config API write. PUT /appearance/rules refuses one.
func (c *Config) AppearanceRuleWarnings() []string {
	var warnings []string
	for _, e := range c.AppearanceRules.Errors() {
		warnings = append(warnings, fmt.Sprintf("appearance_rules[%d]: %s", e.Index, e.Reason))
	}
	return warnings
}

// ValidateAppearanceColor checks that color is a palette name.
func ValidateAppearanceColor(color string) error {
	if !slices.Contains(appearancePalette, color) {
		return fmt.Errorf("color %q is not in the palette (%s)", color, strings.Join(appearancePalette, ", "))
	}
	return nil
}

// ValidateAppearanceEmoji checks that emoji is exactly one grapheme cluster of
// at most MaxAppearanceEmojiBytes bytes.
func ValidateAppearanceEmoji(emoji string) error {
	if len(emoji) > MaxAppearanceEmojiBytes || uniseg.GraphemeClusterCount(emoji) != 1 {
		return fmt.Errorf("emoji %q must be a single emoji (one grapheme cluster of at most %d bytes)", emoji, MaxAppearanceEmojiBytes)
	}
	return nil
}
