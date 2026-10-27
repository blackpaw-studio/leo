package config

import (
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"
)

// OwnedByLeadError refuses a write that would edit or delete an entry the
// follower mirrors from its lead. Op is "edit" or "delete" (a rename deletes
// the old name). Overriding the entry locally first makes it editable.
type OwnedByLeadError struct {
	Kind EntryKind
	Name string
	Lead string
	Op   string
}

func (e *OwnedByLeadError) Error() string {
	return fmt.Sprintf("cannot %s %s %q: it is owned by lead %q (override it locally first, or change it on the lead)", e.Op, e.Kind, e.Name, e.Lead)
}

// followKinds lists the named sections in the order ownership is checked.
var followKinds = []EntryKind{KindTemplate, KindEnvironment, KindProfile}

// CheckLeadOwnership compares every lead entry that is not overridden with
// the lead's copy: a missing one is an attempted delete, a different one an
// attempted edit. Save calls it first, so no write path can change a lead
// entry; callers that want the refusal ahead of validation call it too.
func (c *Config) CheckLeadOwnership() error {
	fl := c.follow
	if fl == nil || fl.cache == nil {
		return nil
	}
	for _, kind := range followKinds {
		for _, name := range c.leadNames(kind) {
			cur, present := c.entryValue(kind, name)
			op := ""
			if !present {
				op = "delete"
			} else if same, err := sameYAML(cur, fl.baselineValue(kind, name)); err != nil {
				return err
			} else if !same {
				op = "edit"
			}
			if op != "" {
				return &OwnedByLeadError{Kind: kind, Name: name, Lead: fl.host, Op: op}
			}
		}
	}
	return nil
}

func (c *Config) entryValue(kind EntryKind, name string) (any, bool) {
	switch kind {
	case KindTemplate:
		v, ok := c.Templates[name]
		return v, ok
	case KindEnvironment:
		v, ok := c.Environments[name]
		return v, ok
	case KindProfile:
		if c.Delegation == nil {
			return nil, false
		}
		v, ok := c.Delegation.Profiles[name]
		return v, ok
	}
	return nil, false
}

func (fl *followLayer) baselineValue(kind EntryKind, name string) any {
	switch kind {
	case KindTemplate:
		return fl.baseline.templates[name]
	case KindEnvironment:
		return fl.baseline.environments[name]
	case KindProfile:
		if fl.baseline.delegation != nil {
			return fl.baseline.delegation.Profiles[name]
		}
	}
	return nil
}

// OverrideLocally turns a lead entry into a local override with the same
// content, so the next Save writes it to leo.yaml where it can be edited. A
// lead entry that is invalid on this host is refused (D6).
func (c *Config) OverrideLocally(kind EntryKind, name string) error {
	if c.follow == nil {
		return fmt.Errorf("cannot override %s %q: this host does not follow a lead", kind, name)
	}
	if reason, ok := c.invalidReason(kind, name); ok {
		return fmt.Errorf("cannot override %s %q: it is invalid on this host (%s); fix it on lead %q", kind, name, reason, c.follow.host)
	}
	switch c.followSource(kind, name) {
	case SourceLead:
		c.follow.sources[kind][name] = SourceLocalOverrideLead
		return nil
	case SourceLocalOverrideLead:
		return fmt.Errorf("%s %q is already overridden locally", kind, name)
	}
	if _, ok := c.entryValue(kind, name); ok {
		return fmt.Errorf("%s %q is not a lead entry", kind, name)
	}
	return &UnknownEntryError{Kind: kind, Name: name}
}

// IsLeadEntry reports whether the named entry is mirrored from the lead and
// not overridden locally.
func (c *Config) IsLeadEntry(kind EntryKind, name string) bool {
	return c.followSource(kind, name) == SourceLead
}

// FollowLead names the lead host, "" when not following.
func (c *Config) FollowLead() string {
	if c.follow == nil {
		return ""
	}
	return c.follow.host
}

// InheritFollow makes c, a config rebuilt from scratch (the setup wizard),
// keep from's follow layer: the follow block, and the lead entries the
// rebuild did not carry over, so the save neither writes them nor reads
// their absence as a delete.
func (c *Config) InheritFollow(from *Config) {
	c.Follow, c.follow = from.Follow, from.follow
	if from.follow == nil || from.follow.cache == nil {
		return
	}
	for _, name := range from.leadNames(KindEnvironment) {
		if _, ok := c.Environments[name]; !ok {
			if c.Environments == nil {
				c.Environments = map[string]map[string]string{}
			}
			c.Environments[name] = from.Environments[name]
		}
	}
	if c.Delegation == nil && from.Delegation != nil {
		d := *from.Delegation
		d.Profiles = map[string]Profile{}
		for _, name := range from.leadNames(KindProfile) {
			d.Profiles[name] = from.Delegation.Profiles[name]
		}
		c.Delegation = &d
	}
}

// localView is the part of c that lives in leo.yaml: lead entries removed,
// and defaults and delegation scalars cut down to the keys set locally at
// load or changed since (D1). c itself is not modified.
func (c *Config) localView() (*Config, error) {
	fl := c.follow
	if fl == nil || fl.cache == nil {
		return c, nil
	}
	v := *c
	v.Templates = withoutLead(c.Templates, fl.sources[KindTemplate])
	v.Environments = withoutLead(c.Environments, fl.sources[KindEnvironment])
	defaults, err := fl.localDefaults(c.Defaults)
	if err != nil {
		return nil, err
	}
	v.Defaults = defaults
	if v.Delegation, err = fl.localDelegation(c.Delegation); err != nil {
		return nil, err
	}
	return &v, nil
}

// withoutLead returns m minus its lead-sourced entries.
func withoutLead[T any](m map[string]T, sources map[string]TemplateSource) map[string]T {
	out := make(map[string]T, len(m))
	for name, v := range m {
		if sources[name] != SourceLead {
			out[name] = v
		}
	}
	if len(out) == 0 && m == nil {
		return nil
	}
	return out
}

func (fl *followLayer) localDefaults(cur DefaultsConfig) (DefaultsConfig, error) {
	var n yaml.Node
	if err := n.Encode(cur); err != nil {
		return DefaultsConfig{}, err
	}
	kept, err := keptKeys(&n, fl.loadedDefaults, fl.localDefaultKeys, followDeepKeys, "")
	if err != nil {
		return DefaultsConfig{}, err
	}
	var out DefaultsConfig
	err = kept.Decode(&out)
	return out, err
}

func (fl *followLayer) localDelegation(cur *DelegationConfig) (*DelegationConfig, error) {
	if fl.leadDelegation == nil || cur == nil {
		return cur, nil
	}
	var n yaml.Node
	if err := n.Encode(cur); err != nil {
		return nil, err
	}
	kept, err := keptKeys(withoutKey(&n, "profiles"), fl.loadedDelegation, fl.localDelegationKeys, nil, "")
	if err != nil {
		return nil, err
	}
	profiles := withoutLead(cur.Profiles, fl.sources[KindProfile])
	if len(kept.Content) == 0 && len(profiles) == 0 {
		return nil, nil
	}
	var out DelegationConfig
	if err := kept.Decode(&out); err != nil {
		return nil, err
	}
	out.Profiles = profiles
	return &out, nil
}

// keptKeys returns cur's keys that were set locally at load, or whose value
// differs from loaded (the effective value at load). A key in deep with a
// mapping value is filtered one level deeper.
func keptKeys(cur, loaded *yaml.Node, local map[string]bool, deep map[string]bool, prefix string) (*yaml.Node, error) {
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if cur = resolveAlias(cur); cur == nil || cur.Kind != yaml.MappingNode {
		return out, nil
	}
	for i := 0; i+1 < len(cur.Content); i += 2 {
		key, val := cur.Content[i], cur.Content[i+1]
		was := mappingValue(loaded, key.Value)
		if !local[prefix+key.Value] && deep[key.Value] && val.Kind == yaml.MappingNode {
			sub, err := keptKeys(val, was, local, nil, prefix+key.Value+".")
			if err != nil {
				return nil, err
			}
			if len(sub.Content) > 0 {
				out.Content = append(out.Content, key, sub)
			}
			continue
		}
		keep := local[prefix+key.Value] || was == nil
		if !keep {
			same, err := sameYAML(val, was)
			if err != nil {
				return nil, err
			}
			keep = !same
		}
		if keep {
			out.Content = append(out.Content, key, val)
		}
	}
	return out, nil
}

// mappingValue returns the value under key in a mapping node, nil if none.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n = resolveAlias(n); n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// snapshotLoaded records the effective defaults and delegation scalars at
// load, the baseline localView compares against to find edits.
func (fl *followLayer) snapshotLoaded(c *Config) {
	var d yaml.Node
	if d.Encode(c.Defaults) == nil {
		fl.loadedDefaults = &d
	}
	if c.Delegation != nil {
		var g yaml.Node
		if g.Encode(c.Delegation) == nil {
			fl.loadedDelegation = withoutKey(&g, "profiles")
		}
	}
}

// saveFollower writes a follower's leo.yaml without a templates_file,
// skipping the write when the local view matches the file as loaded, so a
// no-op save leaves leo.yaml byte-identical.
func saveFollower(path string, view *Config) error {
	fl := view.follow
	data, err := yaml.Marshal(view)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	loaded, err := parse(path, fl.leoBytes)
	if err != nil {
		return err
	}
	if same, err := sameYAML(loaded, view); err != nil {
		return err
	} else if same {
		return nil
	}
	if err := writeConfigFile(path, data, false); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fl.leoBytes = slices.Clone(data)
	return nil
}

// isSharedSource reports whether a template source lives in the
// templates_file.
func isSharedSource(s TemplateSource) bool {
	return s == SourceShared || s == SourceSharedOverrideLead
}
