package config

import (
	"fmt"
	"maps"

	"gopkg.in/yaml.v3"
)

// EntryKind names a section whose entries a follower mirrors by name.
type EntryKind string

const (
	KindTemplate    EntryKind = "template"
	KindEnvironment EntryKind = "environment"
	KindProfile     EntryKind = "profile"
)

// Sources of entries on a follower, beside the templates_file ones.
const (
	SourceLead               TemplateSource = "lead"
	SourceLocalOverrideLead  TemplateSource = "local-override-lead"
	SourceSharedOverrideLead TemplateSource = "shared-override-lead"
)

// followDeepKeys are the defaults keys merged one level deeper (D2): a local
// dispatch.max_concurrent overrides that sub-key only. Every other key,
// harness_options included, is replaced whole.
var followDeepKeys = map[string]bool{"dispatch": true, "push": true}

// followLayer is the load-time record of a follower's lead layer. Like
// sharedTemplates it hangs off Config as an unexported pointer, so yaml and
// json skip it and shallow copies keep it.
type followLayer struct {
	host string
	// cache is the cache the layer came from; nil when there is no lead
	// layer (no cache yet, unreadable, or pulled from another host).
	cache *FollowCache
	// baseline is a second, expanded decode of the lead sections, to tell an
	// edit of a lead entry from an untouched one; it shares no storage with
	// the effective config.
	baseline leadSections
	// leadDefaults and leadDelegation are the lead's nodes, for saving only
	// the keys the follower set or changed.
	leadDefaults, leadDelegation *yaml.Node
	// sources holds the source of every entry the lead layer touched
	// (lead, *-override-lead); other entries keep their usual source.
	sources map[EntryKind]map[string]TemplateSource
	// localDefaultKeys and localDelegationKeys are the dotted keys set
	// locally at load (present and not empty).
	localDefaultKeys, localDelegationKeys map[string]bool
	// leoBytes is leo.yaml as loaded, the baseline for "did leo.yaml change".
	leoBytes []byte
	warnings []string
}

func (fl *followLayer) warn(format string, args ...any) {
	fl.warnings = append(fl.warnings, fmt.Sprintf(format, args...))
}

// FollowWarnings lists why the lead layer is missing or partial; empty for
// a non-follower or a healthy layer.
func (c *Config) FollowWarnings() []string {
	if c.follow == nil {
		return nil
	}
	return append([]string(nil), c.follow.warnings...)
}

// FollowCacheInfo returns the cache the lead layer came from, nil when not
// following or when no cache is in use.
func (c *Config) FollowCacheInfo() *FollowCache {
	if c.follow == nil || c.follow.cache == nil {
		return nil
	}
	fc := *c.follow.cache
	return &fc
}

// EntrySource reports where the named effective entry of kind comes from;
// "" when there is no such entry.
func (c *Config) EntrySource(kind EntryKind, name string) TemplateSource {
	switch kind {
	case KindTemplate:
		return c.TemplateSource(name)
	case KindEnvironment:
		if _, ok := c.Environments[name]; !ok {
			return ""
		}
	case KindProfile:
		if c.Delegation == nil {
			return ""
		}
		if _, ok := c.Delegation.Profiles[name]; !ok {
			return ""
		}
	default:
		return ""
	}
	if s := c.followSource(kind, name); s != "" {
		return s
	}
	return SourceLocal
}

func (c *Config) followSource(kind EntryKind, name string) TemplateSource {
	if c.follow == nil {
		return ""
	}
	return c.follow.sources[kind][name]
}

// applyFollowLayer layers the cached lead config under the local one:
// templates, environments and delegation profiles by name; defaults and
// delegation scalars key by key. A missing, unreadable or foreign cache
// leaves local config only, with a warning.
func (c *Config) applyFollowLayer(leoBytes []byte) {
	fl := &followLayer{
		host:     c.Follow.Host,
		leoBytes: leoBytes,
		sources: map[EntryKind]map[string]TemplateSource{
			KindTemplate: {}, KindEnvironment: {}, KindProfile: {},
		},
	}
	c.follow = fl
	cache, err := ReadFollowCache(c.StatePath())
	switch {
	case err != nil:
		fl.warn("follow cache unreadable (%v); using local config only until the next pull", err)
		return
	case cache == nil:
		fl.warn("following %q but no lead config cached yet; using local config only (run `leo sync`)", fl.host)
		return
	case cache.Host != fl.host:
		fl.warn("follow cache was cached from %q, not %q; ignoring it until the next pull", cache.Host, fl.host)
		return
	}
	exp, effective, err := parseFollowExport(cache.Export)
	if err != nil {
		fl.warn("follow cache is unusable (%v); using local config only until the next pull", err)
		return
	}
	baseline, err := exp.decodeSections()
	if err != nil {
		fl.warn("follow cache is unusable (%v); using local config only until the next pull", err)
		return
	}
	expandTemplates(effective.templates)
	expandTemplates(baseline.templates)
	leadDefaults, err := jsonToNode(exp.Defaults)
	if err != nil {
		fl.warn("follow cache is unusable (%v); using local config only until the next pull", err)
		return
	}
	var leadDelegation *yaml.Node
	if exp.Delegation != nil {
		if leadDelegation, err = jsonToNode(exp.Delegation); err != nil {
			fl.warn("follow cache is unusable (%v); using local config only until the next pull", err)
			return
		}
	}
	localDefaults, localDelegation := localSectionNodes(leoBytes)
	defaults, defaultKeys, err := overlayDefaults(leadDefaults, localDefaults)
	if err != nil {
		fl.warn("lead defaults are unusable (%v); using local defaults only", err)
		defaults, defaultKeys = c.Defaults, nil
	}

	fl.cache, fl.baseline = cache, baseline
	fl.leadDefaults, fl.leadDelegation = leadDefaults, leadDelegation
	fl.localDefaultKeys = defaultKeys
	c.Defaults = defaults
	c.layerTemplates(effective.templates)
	c.Environments = layerByName(effective.environments, c.Environments, fl.sources[KindEnvironment])
	if err := c.layerDelegation(effective.delegation, leadDelegation, localDelegation); err != nil {
		fl.warn("lead delegation is unusable (%v); using local delegation only", err)
	}
}

// layerTemplates puts lead templates under the shared+local merge.
func (c *Config) layerTemplates(lead map[string]TemplateConfig) {
	if len(lead) == 0 {
		return
	}
	before := make(map[string]TemplateSource, len(c.Templates))
	for name := range c.Templates {
		before[name] = c.TemplateSource(name)
	}
	merged := make(map[string]TemplateConfig, len(lead)+len(c.Templates))
	sources := c.follow.sources[KindTemplate]
	for name, tmpl := range lead {
		merged[name] = tmpl
		sources[name] = SourceLead
	}
	for name, tmpl := range c.Templates {
		if _, isLead := lead[name]; isLead {
			switch before[name] {
			case SourceShared:
				sources[name] = SourceSharedOverrideLead
			case SourceLocalOverride:
				delete(sources, name) // labelled after the nearest layer, shared
			default:
				sources[name] = SourceLocalOverrideLead
			}
		}
		merged[name] = tmpl
	}
	c.Templates = merged
}

// layerByName returns lead with every local entry replacing the lead entry of
// the same name whole, recording each lead-touched entry's source.
func layerByName[T any](lead, local map[string]T, sources map[string]TemplateSource) map[string]T {
	if len(lead) == 0 {
		return local
	}
	merged := make(map[string]T, len(lead)+len(local))
	for name, v := range lead {
		merged[name] = v
		sources[name] = SourceLead
	}
	for name, v := range local {
		if _, isLead := lead[name]; isLead {
			sources[name] = SourceLocalOverrideLead
		}
		merged[name] = v
	}
	return merged
}

// layerDelegation merges the lead's delegation under the local one: the
// scalar keys (enabled, active_profile, roles, hide_native_agents) key by
// key, profiles by name.
func (c *Config) layerDelegation(lead *DelegationConfig, leadNode, localNode *yaml.Node) error {
	if lead == nil {
		return nil
	}
	fl := c.follow
	if c.Delegation == nil {
		c.Delegation = lead
		for name := range lead.Profiles {
			fl.sources[KindProfile][name] = SourceLead
		}
		return nil
	}
	merged, keys := overlayKeys(withoutKey(leadNode, "profiles"), withoutKey(localNode, "profiles"), nil, "")
	var d DelegationConfig
	if err := merged.Decode(&d); err != nil {
		return err
	}
	d.Profiles = layerByName(lead.Profiles, c.Delegation.Profiles, fl.sources[KindProfile])
	fl.localDelegationKeys = keys
	c.Delegation = &d
	return nil
}

// overlayDefaults merges the lead's defaults node with the local one (D2
// depth) and decodes the result.
func overlayDefaults(lead, local *yaml.Node) (DefaultsConfig, map[string]bool, error) {
	merged, keys := overlayKeys(lead, local, followDeepKeys, "")
	var d DefaultsConfig
	if err := merged.Decode(&d); err != nil {
		return DefaultsConfig{}, nil, err
	}
	return d, keys, nil
}

// overlayKeys returns a mapping holding lead's keys with each local key that
// is present and not empty replacing lead's value. For a key in deep whose
// values are both mappings, the overlay goes one level deeper. keys records
// the dotted local keys that won.
func overlayKeys(lead, local *yaml.Node, deep map[string]bool, prefix string) (*yaml.Node, map[string]bool) {
	keys := map[string]bool{}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	index := map[string]int{}
	if lead = resolveAlias(lead); lead != nil && lead.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(lead.Content); i += 2 {
			index[lead.Content[i].Value] = len(out.Content) + 1
			out.Content = append(out.Content, lead.Content[i], lead.Content[i+1])
		}
	}
	if local = resolveAlias(local); local == nil || local.Kind != yaml.MappingNode {
		return out, keys
	}
	for i := 0; i+1 < len(local.Content); i += 2 {
		key, val := local.Content[i].Value, resolveAlias(local.Content[i+1])
		if isEmptyNode(val) {
			continue
		}
		at, inLead := index[key]
		if inLead && deep[key] && val.Kind == yaml.MappingNode && resolveAlias(out.Content[at]).Kind == yaml.MappingNode {
			sub, subKeys := overlayKeys(out.Content[at], val, nil, prefix+key+".")
			out.Content[at] = sub
			maps.Copy(keys, subKeys)
			continue
		}
		keys[prefix+key] = true
		if inLead {
			out.Content[at] = val
		} else {
			out.Content = append(out.Content, local.Content[i], val)
		}
	}
	return out, keys
}

// isEmptyNode reports whether a local value counts as unset: null, "", 0,
// {} or []. Save writes `model: ""` and `max_turns: 0` unconditionally, so
// those must not hide the lead's values.
func isEmptyNode(n *yaml.Node) bool {
	if n == nil {
		return true
	}
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		return len(n.Content) == 0
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return true
		case "!!str":
			return n.Value == ""
		case "!!int", "!!float":
			var f float64
			return n.Decode(&f) == nil && f == 0
		}
	}
	return false
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// withoutKey returns a mapping like n minus key; nil stays nil.
func withoutKey(n *yaml.Node, key string) *yaml.Node {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return n
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != key {
			out.Content = append(out.Content, n.Content[i], n.Content[i+1])
		}
	}
	return out
}

// localSectionNodes returns leo.yaml's raw defaults and delegation nodes,
// nil when absent. leo.yaml already parsed once, so errors cannot occur.
func localSectionNodes(leoBytes []byte) (defaults, delegation *yaml.Node) {
	var doc yaml.Node
	if yaml.Unmarshal(leoBytes, &doc) != nil {
		return nil, nil
	}
	root := resolveAlias(documentRoot(&doc))
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "defaults":
			defaults = root.Content[i+1]
		case "delegation":
			delegation = root.Content[i+1]
		}
	}
	return defaults, delegation
}
