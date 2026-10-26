package config

import (
	"bytes"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// entryRange is the line span of one template entry in the shared file:
// start is its first head-comment line (or its key line), keyLine the line of
// the key, end its last content line. Trailing blank lines are outside it, so
// the separators between entries survive an edit. All indexes are 0-based
// and inclusive.
type entryRange struct {
	name                string
	col                 int
	start, keyLine, end int
}

// spliceShared applies edits to the original shared-file bytes, replacing only
// the lines of the entries that changed (re-encoded at the file's indentation
// and column, original key order and comments kept where the nodes allow),
// removing deleted entries with one adjacent separator, and appending new ones
// after the last entry with the file's own separator convention. It reports
// false, so the caller falls back to re-encoding the document, when the
// structure cannot be range-mapped safely: CRLF endings, a missing, empty or
// flow-style `templates:`, anchors, aliases or merge keys, entries that do not
// share one column, or a result that does not decode to the intended
// templates.
func (sh *sharedTemplates) spliceShared(edits sharedEdits) ([]byte, bool) {
	if bytes.Contains(sh.sharedBytes, []byte("\r")) {
		return nil, false
	}
	lines := splitLinesKeepEnds(string(sh.sharedBytes))
	mapping := findTemplatesMapping(&sh.doc)
	entries, ok := mapEntries(lines, mapping)
	if !ok {
		return nil, false
	}
	indent := detectIndent(sh.sharedBytes)

	lines, ok = spliceEdits(lines, mapping, entries, edits, indent)
	if !ok {
		return nil, false
	}
	if len(edits.added) > 0 {
		if lines, ok = spliceAdds(lines, edits, indent); !ok {
			return nil, false
		}
	}
	out := strings.Join(lines, "")
	if !sh.decodesTo(out, edits) {
		return nil, false
	}
	return []byte(out), true
}

// decodesTo checks that data parses to the loaded shared templates with edits
// applied, the guard that makes a mis-mapped range fall back instead of
// corrupting the file.
func (sh *sharedTemplates) decodesTo(data string, edits sharedEdits) bool {
	_, node, err := parseSharedTemplates([]byte(data))
	if err != nil {
		return false
	}
	got, err := decodeTemplates(node)
	if err != nil {
		return false
	}
	expandTemplates(got)

	want := make(map[string]TemplateConfig, len(sh.baseline)+len(edits.added))
	maps.Copy(want, sh.baseline)
	for name := range edits.deleted {
		delete(want, name)
	}
	for oldName, newName := range edits.renamed {
		want[newName] = want[oldName]
		delete(want, oldName)
	}
	for name, cur := range edits.current {
		if newName, ok := edits.renamed[name]; ok {
			name = newName
		}
		cur.AddDirs = slices.Clone(cur.AddDirs) // expandTemplatePaths edits it in place
		want[name] = expandTemplatePaths(cur)
	}
	same, err := sameYAML(got, want)
	return err == nil && same
}

// edit is one replacement of lines [start, end] (inclusive) by repl.
type edit struct {
	start, end int
	repl       []string
}

// spliceEdits rewrites changed entries and removes deleted ones, bottom-up so
// earlier line numbers stay valid.
func spliceEdits(lines []string, mapping *yaml.Node, entries []entryRange, edits sharedEdits, indent int) ([]string, bool) {
	claimed := map[int]bool{}
	claim := func(from, to int) {
		for i := from; i <= to; i++ {
			claimed[i] = true
		}
	}
	var todo []edit
	for i, entry := range entries {
		newName, renamed := edits.renamed[entry.name]
		switch tmpl, changed := edits.changed[entry.name]; {
		case changed:
			if !renamed {
				newName = entry.name
			}
			repl, ok := encodeChangedEntry(mapping, i, tmpl, newName, entry, lines, indent)
			if !ok {
				return nil, false
			}
			todo = append(todo, edit{entry.keyLine, entry.end, repl})
			claim(entry.keyLine, entry.end)
		case renamed:
			line, ok := renameKeyLine(lines[entry.keyLine], entry.col, entry.name, newName)
			if !ok {
				return nil, false
			}
			todo = append(todo, edit{entry.keyLine, entry.keyLine, []string{line}})
			claim(entry.keyLine, entry.keyLine)
		case edits.deleted[entry.name]:
			start, end := entry.start, entry.end
			switch {
			case end+1 < len(lines) && isBlankLine(lines[end+1]) && !claimed[end+1]:
				end++
			case start > 0 && isBlankLine(lines[start-1]) && !claimed[start-1]:
				start--
			}
			todo = append(todo, edit{start, end, nil})
			claim(start, end)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].start > todo[j].start })
	for _, e := range todo {
		lines = slices.Concat(lines[:e.start], e.repl, lines[e.end+1:])
	}
	return lines, true
}

// spliceAdds appends the added entries after the last remaining entry. The
// text is re-parsed first: earlier edits may have moved every line.
func spliceAdds(lines []string, edits sharedEdits, indent int) ([]string, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines, "")), &doc); err != nil {
		return nil, false
	}
	entries, ok := mapEntries(lines, findTemplatesMapping(&doc))
	if !ok {
		return nil, false
	}
	last := entries[len(entries)-1]
	useBlank := false
	for i := 0; i+1 < len(entries); i++ {
		for _, line := range lines[entries[i].end+1 : entries[i+1].start] {
			useBlank = useBlank || isBlankLine(line)
		}
	}

	var insert []string
	for _, name := range slices.Sorted(maps.Keys(edits.added)) {
		var val yaml.Node
		if err := val.Encode(edits.added[name]); err != nil {
			return nil, false
		}
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
		entry, ok := encodeEntryLines(key, &val, last.col, indent)
		if !ok {
			return nil, false
		}
		if useBlank {
			insert = append(insert, "\n")
		}
		insert = append(insert, entry...)
	}
	if !strings.HasSuffix(lines[last.end], "\n") {
		lines[last.end] += "\n"
	}
	return slices.Concat(lines[:last.end+1], insert, lines[last.end+1:]), true
}

// encodeChangedEntry renders the entry at index i with its new template: the
// original value node is kept key by key, so untouched fields keep their text
// style, comments and order; changed fields take the new value, cleared ones
// disappear and new ones go last.
func encodeChangedEntry(mapping *yaml.Node, i int, tmpl TemplateConfig, name string, entry entryRange, lines []string, indent int) ([]string, bool) {
	origKey, origVal := mapping.Content[2*i], mapping.Content[2*i+1]
	var fresh yaml.Node
	if err := fresh.Encode(tmpl); err != nil {
		return nil, false
	}
	// yaml.v3 also hangs comments from the lines around an entry on its
	// nodes; the lines outside the replaced range stay put, so only comments
	// written inside it may be emitted again.
	entryText := strings.Join(lines[entry.keyLine:entry.end+1], "")
	val := keepOwnComments(mergeMapping(origVal, &fresh, true), entryText)
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: origKey.Tag, Style: origKey.Style,
		Value: name, LineComment: ownComment(origKey.LineComment, entryText)}
	if name != origKey.Value {
		key.Style = 0
	}
	return encodeEntryLines(key, val, entry.col, indent)
}

// renameKeyLine swaps a plain key at col for newName and leaves the rest of
// the line (value, comment) as written. It reports false for a quoted or
// otherwise unusual key.
func renameKeyLine(line string, col int, oldName, newName string) (string, bool) {
	rest, ok := strings.CutPrefix(line[col:], oldName+":")
	if !ok {
		return "", false
	}
	return line[:col] + newName + ":" + rest, true
}

// mergeMapping returns orig with fresh's values applied: keys in orig's order,
// unchanged values (compared structurally, ignoring comments and style) keep
// orig's node, changed mappings are merged recursively, other changed values
// take fresh's node with orig's comments, keys missing from fresh are dropped
// and keys new in fresh are appended. At the top level of a template entry
// (topLevel), keys TemplateConfig does not know are kept as written: the
// loader tolerates them, and a file shared across leo versions may carry
// fields this version has never heard of.
func mergeMapping(orig, fresh *yaml.Node, topLevel bool) *yaml.Node {
	if orig.Kind != yaml.MappingNode || fresh.Kind != yaml.MappingNode {
		return mergeValue(orig, fresh)
	}
	freshByKey := make(map[string]*yaml.Node, len(fresh.Content)/2)
	for i := 0; i+1 < len(fresh.Content); i += 2 {
		freshByKey[fresh.Content[i].Value] = fresh.Content[i+1]
	}
	out := *orig
	out.Content = nil
	seen := map[string]bool{}
	for i := 0; i+1 < len(orig.Content); i += 2 {
		key, val := orig.Content[i], orig.Content[i+1]
		next, ok := freshByKey[key.Value]
		if !ok {
			if topLevel && !templateYAMLKeys()[key.Value] {
				out.Content = append(out.Content, key, val)
			}
			continue
		}
		seen[key.Value] = true
		if !nodesEqual(val, next) {
			val = mergeValue(val, next)
		}
		out.Content = append(out.Content, key, val)
	}
	for i := 0; i+1 < len(fresh.Content); i += 2 {
		if !seen[fresh.Content[i].Value] {
			out.Content = append(out.Content, fresh.Content[i], fresh.Content[i+1])
		}
	}
	return &out
}

// keepOwnComments returns a deep copy of n keeping only the comments whose
// text appears in entryText.
func keepOwnComments(n *yaml.Node, entryText string) *yaml.Node {
	c := *n
	c.HeadComment = ownComment(n.HeadComment, entryText)
	c.LineComment = ownComment(n.LineComment, entryText)
	c.FootComment = ownComment(n.FootComment, entryText)
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = keepOwnComments(child, entryText)
	}
	return &c
}

// ownComment is comment if every line of it occurs in text, else "".
func ownComment(comment, text string) string {
	for _, line := range strings.Split(comment, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.Contains(text, line) {
			return ""
		}
	}
	return comment
}

// mergeValue is the node for a changed value: a merge for mappings, so nested
// keys keep their order and comments, otherwise fresh's node with orig's
// comments.
func mergeValue(orig, fresh *yaml.Node) *yaml.Node {
	if orig.Kind == yaml.MappingNode && fresh.Kind == yaml.MappingNode {
		return mergeMapping(orig, fresh, false)
	}
	merged := *fresh
	merged.HeadComment, merged.LineComment, merged.FootComment = orig.HeadComment, orig.LineComment, orig.FootComment
	return &merged
}

// nodesEqual compares two nodes by meaning: scalars by resolved type and
// text, sequences in order, mappings by key. Comments and styles are ignored.
func nodesEqual(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || len(a.Content) != len(b.Content) {
		return false
	}
	switch a.Kind {
	case yaml.ScalarNode:
		return a.ShortTag() == b.ShortTag() && a.Value == b.Value
	case yaml.SequenceNode:
		for i := range a.Content {
			if !nodesEqual(a.Content[i], b.Content[i]) {
				return false
			}
		}
		return true
	case yaml.MappingNode:
		byKey := make(map[string]*yaml.Node, len(b.Content)/2)
		for i := 0; i+1 < len(b.Content); i += 2 {
			byKey[b.Content[i].Value] = b.Content[i+1]
		}
		for i := 0; i+1 < len(a.Content); i += 2 {
			other, ok := byKey[a.Content[i].Value]
			if !ok || !nodesEqual(a.Content[i+1], other) {
				return false
			}
		}
		return true
	}
	return false
}

// encodeEntryLines encodes one `key: value` entry at the given indentation
// step and shifts it right to the entry's column.
func encodeEntryLines(key, val *yaml.Node, col, indent int) ([]string, bool) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{key, val}}
	if err := enc.Encode(entry); err != nil {
		return nil, false
	}
	if err := enc.Close(); err != nil {
		return nil, false
	}
	pad := strings.Repeat(" ", col)
	lines := splitLinesKeepEnds(buf.String())
	for i, line := range lines {
		if !isBlankLine(line) {
			lines[i] = pad + line
		}
	}
	return lines, true
}

// mapEntries computes each entry's line range from the parsed key positions.
// It reports false for anything it cannot map with confidence.
func mapEntries(lines []string, mapping *yaml.Node) ([]entryRange, bool) {
	if mapping == nil || len(mapping.Content) == 0 || mapping.Style&yaml.FlowStyle != 0 || hasAnchorsOrAliases(mapping) {
		return nil, false
	}
	entries := make([]entryRange, 0, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		keyLine, col := key.Line-1, key.Column-1
		if key.Kind != yaml.ScalarNode || keyLine < 0 || keyLine >= len(lines) || leadingSpaces(lines[keyLine]) != col {
			return nil, false
		}
		if len(entries) > 0 && (entries[0].col != col || keyLine <= entries[len(entries)-1].keyLine) {
			return nil, false
		}
		entries = append(entries, entryRange{
			name: key.Value, col: col, keyLine: keyLine,
			start: headCommentStart(lines, keyLine, col), end: entryEnd(lines, keyLine, col),
		})
	}
	return entries, true
}

// headCommentStart extends keyLine upward over comment lines directly above
// it at the key's own column (a head comment; a blank line detaches it).
func headCommentStart(lines []string, keyLine, col int) int {
	start := keyLine
	for start > 0 && leadingSpaces(lines[start-1]) == col && isCommentLine(lines[start-1]) {
		start--
	}
	return start
}

// entryEnd is the last non-blank line after keyLine that belongs to the
// entry: one indented deeper than the key (comments included), or a comment
// at or left of the key column that is followed by such a line. The entry
// stops at the first line that is neither.
func entryEnd(lines []string, keyLine, col int) int {
	end := keyLine
	for i := keyLine + 1; i < len(lines); i++ {
		switch {
		case isBlankLine(lines[i]):
		case leadingSpaces(lines[i]) > col:
			end = i
		case isCommentLine(lines[i]):
			// At or left of the key column a comment belongs to the entry
			// only if more of it follows (a commented-out field at column 0).
			next := nextContentLine(lines, i+1)
			if next < 0 || leadingSpaces(lines[next]) <= col {
				return end
			}
		default:
			return end
		}
	}
	return end
}

// nextContentLine is the index of the first non-blank, non-comment line at or
// after from, or -1.
func nextContentLine(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if !isBlankLine(lines[i]) && !isCommentLine(lines[i]) {
			return i
		}
	}
	return -1
}

func isCommentLine(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

func hasAnchorsOrAliases(n *yaml.Node) bool {
	if n.Anchor != "" || n.Kind == yaml.AliasNode || (n.Kind == yaml.ScalarNode && n.Value == "<<" && n.Tag == "!!merge") {
		return true
	}
	return slices.ContainsFunc(n.Content, hasAnchorsOrAliases)
}

func splitLinesKeepEnds(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

func leadingSpaces(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func isBlankLine(line string) bool { return strings.TrimSpace(line) == "" }

// templateYAMLKeys is the set of top-level yaml keys TemplateConfig reads,
// derived from its struct tags (inline structs included).
var templateYAMLKeys = sync.OnceValue(func() map[string]bool {
	keys := map[string]bool{}
	addYAMLKeys(reflect.TypeFor[TemplateConfig](), keys)
	return keys
})

func addYAMLKeys(t reflect.Type, keys map[string]bool) {
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		switch {
		case name == "-":
		case slices.Contains(strings.Split(opts, ","), "inline"):
			if field.Type.Kind() == reflect.Struct {
				addYAMLKeys(field.Type, keys)
			}
		case name == "":
			keys[strings.ToLower(field.Name)] = true
		default:
			keys[name] = true
		}
	}
}
