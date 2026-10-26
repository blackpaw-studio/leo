package config

import (
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
	// raw is the shared file as parsed, paths NOT expanded: unchanged
	// templates are written back from it so ~ paths stay portable.
	raw map[string]TemplateConfig
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

// decodeSharedTemplates validates the shared file's shape (a single top-level
// `templates:` key) and returns its template node. Unknown keys inside a
// template are tolerated, as in leo.yaml.
func decodeSharedTemplates(data []byte) (yaml.Node, error) {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return yaml.Node{}, err
	}
	for _, key := range slices.Sorted(maps.Keys(top)) {
		if key != sharedTemplatesKey {
			return yaml.Node{}, fmt.Errorf("unknown top-level key %q (only %q is allowed)", key, sharedTemplatesKey)
		}
	}
	return top[sharedTemplatesKey], nil
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
	node, err := decodeSharedTemplates(data)
	if err != nil {
		return fmt.Errorf("templates_file %s: %w", path, err)
	}
	raw, err := decodeTemplates(node)
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

	sources := make(map[string]TemplateSource, len(raw)+len(c.Templates))
	merged := make(map[string]TemplateConfig, len(raw)+len(c.Templates))
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
		raw:         raw,
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

// sharedTemplatesDoc is the on-disk shape of a templates_file.
type sharedTemplatesDoc struct {
	Templates map[string]TemplateConfig `yaml:"templates"`
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

// marshalShared renders the shared file for cfg's current templates and says
// whether it differs from what was loaded. Entries that were not edited come
// from raw, so hand-written ~ paths survive; edited ones are re-contracted so
// one machine's expanded home never lands in a file other machines read.
func (sh *sharedTemplates) marshalShared(cfg *Config) ([]byte, bool, error) {
	out := make(map[string]TemplateConfig, len(sh.raw)+len(sh.newShared))
	for name, rawTmpl := range sh.raw {
		switch sh.sources[name] {
		case SourceLocalOverride:
			out[name] = rawTmpl
		case SourceShared:
			cur, ok := cfg.Templates[name]
			if !ok {
				continue // deleted or renamed away
			}
			same, err := sameYAML(cur, sh.baseline[name])
			if err != nil {
				return nil, false, err
			}
			if same {
				out[name] = rawTmpl
			} else {
				out[name] = contractHome(cur)
			}
		}
	}
	for name := range sh.newShared {
		if _, done := out[name]; done {
			continue
		}
		if cur, ok := cfg.Templates[name]; ok {
			out[name] = contractHome(cur)
		}
	}
	changed, err := sameYAML(sharedTemplatesDoc{Templates: out}, sharedTemplatesDoc{Templates: sh.raw})
	if err != nil {
		return nil, false, err
	}
	data, err := yaml.Marshal(sharedTemplatesDoc{Templates: out})
	if err != nil {
		return nil, false, fmt.Errorf("marshaling templates_file: %w", err)
	}
	return data, !changed, nil
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
