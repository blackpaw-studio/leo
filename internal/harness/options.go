package harness

// OptionType says how a harness_options key renders in the web UI and how
// its submitted form value converts back into the map shape DecodeOptions
// expects. New types are added only when an adapter needs them (YAGNI).
type OptionType int

const (
	OptionString     OptionType = iota // single-line text → string
	OptionText                         // multi-line text (textarea) → string
	OptionBool                         // tri-state select unset/true/false → bool
	OptionEnum                         // fixed value list → string
	OptionStringList                   // comma-separated input → []any of string
	OptionYAMLMap                      // YAML textarea → map[string]any
)

// OptionField describes one harness_options key for web-form rendering. It
// must stay consistent with the adapter's DecodeOptions: same key set, and
// every EnumValues entry must decode cleanly — locked per adapter by
// schematest.Run.
type OptionField struct {
	Key        string // harness_options key; doubles as the form input suffix
	Label      string
	Help       string
	Type       OptionType
	EnumValues []string // OptionEnum only
	// Source optionally names a web option-source ("agents") whose values
	// populate a select for this field. Loose by-name hint: the web layer
	// falls back to Type's plain control when it can't resolve the name.
	Source string
}
