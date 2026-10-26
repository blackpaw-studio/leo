package config

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// sharedTemplatesKey is the only top-level key a templates_file may hold.
const sharedTemplatesKey = "templates"

// TemplateSource says which file an effective template came from.
type TemplateSource string

const (
	SourceLocal         TemplateSource = "local"
	SourceShared        TemplateSource = "shared"
	SourceLocalOverride TemplateSource = "local-override"
)

// Label is the human-facing form of the source.
func (s TemplateSource) Label() string {
	if s == SourceLocalOverride {
		return "local · overrides shared"
	}
	return string(s)
}

// sharedTemplates is the load-time record of a templates_file. It hangs off
// Config as an unexported pointer, so yaml and json skip it and the shallow
// copies Config goes through (rename, web enable) keep it.
type sharedTemplates struct {
	path string
	// doc is the shared file's parsed YAML document, paths NOT expanded.
	// Saving edits a copy of it, so untouched entries (and the header,
	// comments and key order around them) are written back as they were.
	doc yaml.Node
	// baseline is a second, path-expanded parse used to detect edits. It
	// must not share backing storage with the effective templates, or an
	// in-place edit would compare as unchanged and be lost.
	baseline map[string]TemplateConfig
	sources  map[string]TemplateSource
	// newShared holds names created or renamed into the shared file since load.
	newShared map[string]bool
	// leoBytes is leo.yaml as loaded, the baseline for "did leo.yaml change".
	leoBytes []byte
	// sharedBytes is the shared file as loaded, hashed into the revision.
	sharedBytes []byte
}

// HasTemplatesFile reports whether a shared templates file was loaded.
func (c *Config) HasTemplatesFile() bool { return c.shared != nil }

// InheritTemplatesFile makes c use from's templates_file, so a config rebuilt
// from scratch (the setup wizard) keeps saving shared templates to the shared
// file instead of flattening them into leo.yaml. It is a no-op when from has
// none.
func (c *Config) InheritTemplatesFile(from *Config) {
	c.TemplatesFile = from.TemplatesFile
	c.shared = from.shared
}

// TemplateSource reports where the named effective template lives. Templates
// of a config with no shared file are all local; an unknown name yields "".
func (c *Config) TemplateSource(name string) TemplateSource {
	if _, ok := c.Templates[name]; !ok {
		return ""
	}
	if c.shared == nil {
		return SourceLocal
	}
	if c.shared.newShared[name] {
		return SourceShared
	}
	if s, ok := c.shared.sources[name]; ok {
		return s
	}
	return SourceLocal
}

// resolveTemplatesFile expands ~ and resolves a relative path against home,
// the directory holding leo.yaml.
func resolveTemplatesFile(home, p string) string {
	p = expandHome(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(home, p)
}

// parseSharedTemplates parses the shared file and validates its shape (a
// single top-level `templates:` key). It returns the whole document, for
// later editing, and the `templates` value node. Unknown keys inside a
// template are tolerated, as in leo.yaml.
func parseSharedTemplates(data []byte) (doc, templates yaml.Node, err error) {
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return yaml.Node{}, yaml.Node{}, err
	}
	root := documentRoot(&doc)
	if root == nil {
		return doc, yaml.Node{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return yaml.Node{}, yaml.Node{}, fmt.Errorf("top level must be a mapping with a single %q key", sharedTemplatesKey)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if key != sharedTemplatesKey {
			return yaml.Node{}, yaml.Node{}, fmt.Errorf("unknown top-level key %q (only %q is allowed)", key, sharedTemplatesKey)
		}
		templates = *root.Content[i+1]
	}
	return doc, templates, nil
}

// documentRoot is the top-level node of a parsed document, nil for an empty one.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

func decodeTemplates(node yaml.Node) (map[string]TemplateConfig, error) {
	var tmpls map[string]TemplateConfig
	if node.IsZero() {
		return tmpls, nil
	}
	if err := node.Decode(&tmpls); err != nil {
		return nil, err
	}
	return tmpls, nil
}

// loadSharedTemplates reads cfg.TemplatesFile and merges its templates under
// cfg's local ones: a local template replaces a shared one of the same name
// whole. leoBytes is leo.yaml as parsed.
func (c *Config) loadSharedTemplates(leoBytes []byte) error {
	path := resolveTemplatesFile(c.HomePath, c.TemplatesFile)
	data, err := os.ReadFile(path) //nolint:gosec // path comes from the operator's own leo.yaml
	if err != nil {
		return fmt.Errorf("templates_file %s: %w", path, err)
	}
	doc, node, err := parseSharedTemplates(data)
	if err != nil {
		return fmt.Errorf("templates_file %s: %w", path, err)
	}
	baseline, err := decodeTemplates(node)
	if err != nil {
		return fmt.Errorf("templates_file %s: %w", path, err)
	}
	effective, err := decodeTemplates(node)
	if err != nil {
		return fmt.Errorf("templates_file %s: %w", path, err)
	}
	expandTemplates(baseline)
	expandTemplates(effective)

	sources := make(map[string]TemplateSource, len(baseline)+len(c.Templates))
	merged := make(map[string]TemplateConfig, len(baseline)+len(c.Templates))
	for name, tmpl := range effective {
		merged[name] = tmpl
		sources[name] = SourceShared
	}
	for name, tmpl := range c.Templates {
		if _, ok := merged[name]; ok {
			sources[name] = SourceLocalOverride
		} else {
			sources[name] = SourceLocal
		}
		merged[name] = tmpl
	}
	c.Templates = merged
	c.shared = &sharedTemplates{
		path:        path,
		doc:         doc,
		baseline:    baseline,
		sources:     sources,
		newShared:   map[string]bool{},
		leoBytes:    leoBytes,
		sharedBytes: data,
	}
	return nil
}

// expandTemplates expands ~ in every template's path fields, in place.
func expandTemplates(tmpls map[string]TemplateConfig) {
	for name, tmpl := range tmpls {
		tmpls[name] = expandTemplatePaths(tmpl)
	}
}

// expandTemplatePaths expands ~ in a template's workspace and path fields.
func expandTemplatePaths(tmpl TemplateConfig) TemplateConfig {
	tmpl.Workspace = expandHome(tmpl.Workspace)
	tmpl.MCPConfig = expandHome(tmpl.MCPConfig)
	for i, dir := range tmpl.AddDirs {
		tmpl.AddDirs[i] = expandHome(dir)
	}
	return tmpl
}

// AddTemplate adds a new template, to the shared file when shared is set and
// to leo.yaml otherwise. It mutates c in place and does not save.
func (c *Config) AddTemplate(name string, tmpl TemplateConfig, shared bool) error {
	if _, exists := c.Templates[name]; exists {
		return fmt.Errorf("template %q already exists", name)
	}
	if shared && !c.HasTemplatesFile() {
		return fmt.Errorf("cannot create shared template %q: templates_file is not set", name)
	}
	if c.Templates == nil {
		c.Templates = map[string]TemplateConfig{}
	}
	c.Templates[name] = tmpl
	if shared {
		c.shared.newShared[name] = true
	}
	return nil
}

// saveSplit writes shared-sourced templates to the shared file and everything
// else to leo.yaml, rewriting each file only when its parsed content changed
// so editing a local template never touches a git-tracked shared file.
func saveSplit(path string, cfg *Config) error {
	sh := cfg.shared
	sharedData, sharedChanged, err := sh.marshalShared(cfg)
	if err != nil {
		return err
	}
	leoData, leoChanged, err := sh.marshalLeo(path, cfg)
	if err != nil {
		return err
	}
	if sharedChanged {
		if err := writeConfigFile(sh.path, sharedData, true); err != nil {
			return fmt.Errorf("writing templates_file %s: %w", sh.path, err)
		}
	}
	if leoChanged {
		if err := writeConfigFile(path, leoData, false); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
	}
	return nil
}

// sharedEdits is what a save changes in the shared file.
type sharedEdits struct {
	// changed are existing shared entries to rewrite, ~ paths restored.
	changed map[string]TemplateConfig
	deleted map[string]bool
	// added are new shared entries, ~ paths restored.
	added map[string]TemplateConfig
	// current holds the expanded templates for changed and added entries,
	// to check a rewritten file against what was meant.
	current map[string]TemplateConfig
}

func (e sharedEdits) empty() bool {
	return len(e.changed) == 0 && len(e.deleted) == 0 && len(e.added) == 0
}

// planEdits compares cfg's templates with the shared file as loaded.
func (sh *sharedTemplates) planEdits(cfg *Config) (sharedEdits, error) {
	edits := sharedEdits{
		changed: map[string]TemplateConfig{}, deleted: map[string]bool{},
		added: map[string]TemplateConfig{}, current: map[string]TemplateConfig{},
	}
	present := map[string]bool{}
	if mapping := findTemplatesMapping(&sh.doc); mapping != nil {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			name := mapping.Content[i].Value
			present[name] = true
			if sh.sources[name] != SourceShared {
				continue
			}
			cur, ok := cfg.Templates[name]
			if !ok {
				edits.deleted[name] = true // deleted or renamed away
				continue
			}
			same, err := sameYAML(cur, sh.baseline[name])
			if err != nil {
				return sharedEdits{}, err
			}
			if !same {
				edits.changed[name] = contractHome(cur)
				edits.current[name] = cur
			}
		}
	}
	for name := range sh.newShared {
		if cur, ok := cfg.Templates[name]; ok && !present[name] {
			edits.added[name] = contractHome(cur)
			edits.current[name] = cur
		}
	}
	return edits, nil
}

// marshalShared renders the shared file for cfg's current templates and says
// whether it changed. Only entries that were added, edited or removed are
// touched: the original bytes are spliced where possible (see spliceShared),
// so everything else stays byte-identical; otherwise the parsed document is
// re-encoded (see encodeSharedNodes).
func (sh *sharedTemplates) marshalShared(cfg *Config) ([]byte, bool, error) {
	edits, err := sh.planEdits(cfg)
	if err != nil {
		return nil, false, err
	}
	if edits.empty() {
		return nil, false, nil
	}
	if out, ok := sh.spliceShared(edits); ok {
		return out, true, nil
	}
	return sh.encodeSharedNodes(edits)
}

// encodeSharedNodes is the fallback writer for files whose structure cannot be
// range-mapped (a flow-style `templates:`, anchors or aliases, CRLF endings,
// an empty file). It re-encodes the parsed document at the file's indentation:
// comments, quoting, flow style and key order survive, but blank lines
// between entries, spacing before line comments and a leading `---` do not.
func (sh *sharedTemplates) encodeSharedNodes(edits sharedEdits) ([]byte, bool, error) {
	doc := cloneNode(&sh.doc)
	mapping := templatesMapping(doc)

	content := make([]*yaml.Node, 0, len(mapping.Content))
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, val := mapping.Content[i], mapping.Content[i+1]
		if edits.deleted[key.Value] {
			continue
		}
		if tmpl, ok := edits.changed[key.Value]; ok {
			var err error
			if val, err = replacementValue(val, tmpl); err != nil {
				return nil, false, err
			}
		}
		content = append(content, key, val)
	}
	for _, name := range slices.Sorted(maps.Keys(edits.added)) {
		val, err := replacementValue(&yaml.Node{}, edits.added[name])
		if err != nil {
			return nil, false, err
		}
		content = append(content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, val)
	}
	mapping.Content = content

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(detectIndent(sh.sharedBytes))
	if err := enc.Encode(doc); err != nil {
		return nil, false, fmt.Errorf("marshaling templates_file: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, false, fmt.Errorf("marshaling templates_file: %w", err)
	}
	return buf.Bytes(), true, nil
}

// findTemplatesMapping returns the block or flow mapping under the `templates`
// key, nil when the file has none (empty file, `templates:` with no value).
func findTemplatesMapping(doc *yaml.Node) *yaml.Node {
	root := documentRoot(doc)
	if root == nil {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == sharedTemplatesKey && root.Content[i+1].Kind == yaml.MappingNode {
			return root.Content[i+1]
		}
	}
	return nil
}

// replacementValue encodes tmpl as a value node that takes over old's comments.
func replacementValue(old *yaml.Node, tmpl TemplateConfig) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(tmpl); err != nil {
		return nil, fmt.Errorf("encoding template: %w", err)
	}
	n.HeadComment, n.LineComment, n.FootComment = old.HeadComment, old.LineComment, old.FootComment
	return &n, nil
}

// templatesMapping returns the mapping node holding the templates inside doc,
// creating the document, the `templates` key or its mapping when the file has
// none (empty file, `templates:` with no value).
func templatesMapping(doc *yaml.Node) *yaml.Node {
	root := documentRoot(doc)
	if root == nil {
		*doc = yaml.Node{Kind: yaml.DocumentNode}
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Content = []*yaml.Node{root}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != sharedTemplatesKey {
			continue
		}
		if root.Content[i+1].Kind != yaml.MappingNode {
			root.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		return root.Content[i+1]
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: sharedTemplatesKey}, mapping)
	return mapping
}

// cloneNode deep-copies a node tree so edits never reach the loaded document.
// Alias targets stay shared; they are only read when encoding.
func cloneNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = cloneNode(child)
	}
	return &c
}

// detectIndent is the indentation of the first indented, non-comment line of
// data, clamped to what the encoder supports; 2 when there is none.
func detectIndent(data []byte) int {
	const defaultIndent, minIndent, maxIndent = 2, 2, 8
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if n := len(line) - len(trimmed); n > 0 {
			return min(max(n, minIndent), maxIndent)
		}
	}
	return defaultIndent
}

// marshalLeo renders leo.yaml with only the templates that live there and says
// whether it differs from the leo.yaml that was loaded.
func (sh *sharedTemplates) marshalLeo(path string, cfg *Config) ([]byte, bool, error) {
	local := *cfg
	local.Templates = make(map[string]TemplateConfig, len(cfg.Templates))
	for name, tmpl := range cfg.Templates {
		if cfg.TemplateSource(name) != SourceShared {
			local.Templates[name] = tmpl
		}
	}
	data, err := yaml.Marshal(&local)
	if err != nil {
		return nil, false, fmt.Errorf("marshaling config: %w", err)
	}
	loaded, err := parse(path, sh.leoBytes)
	if err != nil {
		return nil, false, err
	}
	same, err := sameYAML(loaded, &local)
	if err != nil {
		return nil, false, err
	}
	return data, !same, nil
}

// sameYAML reports whether a and b marshal to the same YAML.
func sameYAML(a, b any) (bool, error) {
	ab, err := yaml.Marshal(a)
	if err != nil {
		return false, fmt.Errorf("marshaling for comparison: %w", err)
	}
	bb, err := yaml.Marshal(b)
	if err != nil {
		return false, fmt.Errorf("marshaling for comparison: %w", err)
	}
	return string(ab) == string(bb), nil
}

// contractHome rewrites paths under the user's home as ~/... in a copy of
// tmpl, the inverse of expandTemplatePaths.
func contractHome(tmpl TemplateConfig) TemplateConfig {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return tmpl
	}
	contract := func(p string) string {
		switch {
		case p == home:
			return "~"
		case strings.HasPrefix(p, home+string(filepath.Separator)):
			return "~/" + filepath.ToSlash(strings.TrimPrefix(p, home+string(filepath.Separator)))
		}
		return p
	}
	tmpl.Workspace = contract(tmpl.Workspace)
	tmpl.MCPConfig = contract(tmpl.MCPConfig)
	tmpl.AddDirs = slices.Clone(tmpl.AddDirs)
	for i, dir := range tmpl.AddDirs {
		tmpl.AddDirs[i] = contract(dir)
	}
	return tmpl
}
