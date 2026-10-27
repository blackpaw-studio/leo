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
	defaults, defaultEmpties, err := fl.localDefaults(c.Defaults)
	if err != nil {
		return nil, err
	}
	v.Defaults = defaults
	delegation, delegationEmpties, err := fl.localDelegation(c.Delegation)
	if err != nil {
		return nil, err
	}
	v.Delegation = delegation
	v.localEmpties = newLocalEmpties(defaultEmpties, delegationEmpties)
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

// localDefaults returns the defaults keys leo.yaml keeps, and those of them
// that are empty.
func (fl *followLayer) localDefaults(cur DefaultsConfig) (DefaultsConfig, *yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(cur); err != nil {
		return DefaultsConfig{}, nil, err
	}
	kept, err := keptKeys(&n, fl.loadedDefaults, fl.localDefaultKeys, followDeepKeys, "")
	if err != nil {
		return DefaultsConfig{}, nil, err
	}
	var out DefaultsConfig
	if err := kept.Decode(&out); err != nil {
		return DefaultsConfig{}, nil, err
	}
	return out, presentEmpties(kept, followDeepKeys), nil
}

// localDelegation returns the delegation leo.yaml keeps, and its scalar
// keys that are empty.
func (fl *followLayer) localDelegation(cur *DelegationConfig) (*DelegationConfig, *yaml.Node, error) {
	if fl.leadDelegation == nil || cur == nil {
		return cur, nil, nil
	}
	var n yaml.Node
	if err := n.Encode(cur); err != nil {
		return nil, nil, err
	}
	kept, err := keptKeys(withoutKey(&n, "profiles"), fl.loadedDelegation, fl.localDelegationKeys, nil, "")
	if err != nil {
		return nil, nil, err
	}
	profiles := withoutLead(cur.Profiles, fl.sources[KindProfile])
	if len(kept.Content) == 0 && len(profiles) == 0 {
		return nil, nil, nil
	}
	var out DelegationConfig
	if err := kept.Decode(&out); err != nil {
		return nil, nil, err
	}
	out.Profiles = profiles
	return &out, presentEmpties(kept, nil), nil
}

// keptKeys returns cur's keys that were set locally at load, or whose value
// differs from loaded (the effective value at load). A key in deep with a
// mapping value is filtered one level deeper. A key loaded has and cur
// lacks was cleared, and omitempty dropped it: it is kept as an explicit
// empty value, so the lead's value does not come back.
func keptKeys(cur, loaded *yaml.Node, local map[string]bool, deep map[string]bool, prefix string) (*yaml.Node, error) {
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if cur = resolveAlias(cur); cur == nil || cur.Kind != yaml.MappingNode {
		cur = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
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
	return clearedKeys(out, cur, loaded, local, deep, prefix)
}

// clearedKeys adds to out each key of loaded that cur lacks, as an empty
// value of the loaded value's shape.
func clearedKeys(out, cur, loaded *yaml.Node, local, deep map[string]bool, prefix string) (*yaml.Node, error) {
	if loaded = resolveAlias(loaded); loaded == nil || loaded.Kind != yaml.MappingNode {
		return out, nil
	}
	for i := 0; i+1 < len(loaded.Content); i += 2 {
		key, was := loaded.Content[i], resolveAlias(loaded.Content[i+1])
		if mappingValue(cur, key.Value) != nil {
			continue
		}
		if deep[key.Value] && was.Kind == yaml.MappingNode {
			sub, err := keptKeys(nil, was, local, nil, prefix+key.Value+".")
			if err != nil {
				return nil, err
			}
			if len(sub.Content) > 0 {
				out.Content = append(out.Content, key, sub)
			}
			continue
		}
		if e := emptyLike(was); e != nil {
			out.Content = append(out.Content, key, e)
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
// load, the baseline localView compares against to find edits. The empty
// keys leo.yaml sets are added back, since encoding drops them.
func (fl *followLayer) snapshotLoaded(c *Config) {
	empties := fileEmpties(fl.leoBytes)
	var d yaml.Node
	if d.Encode(c.Defaults) == nil {
		fl.loadedDefaults = &d
		if empties != nil {
			addAbsent(fl.loadedDefaults, empties.defaults)
		}
	}
	if c.Delegation != nil {
		var g yaml.Node
		if g.Encode(c.Delegation) == nil {
			fl.loadedDelegation = withoutKey(&g, "profiles")
			if empties != nil {
				addAbsent(fl.loadedDelegation, empties.delegation)
			}
		}
	}
}

// saveFollower writes a follower's leo.yaml without a templates_file,
// skipping the write when the local view matches the file as loaded, so a
// no-op save leaves leo.yaml byte-identical.
func saveFollower(path string, view *Config) error {
	fl := view.follow
	data, err := marshalLocal(view)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if same, err := sameAsLoaded(path, fl.leoBytes, view, data); err != nil {
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

// sameAsLoaded reports whether data, the marshaled view, holds the same
// config as leoBytes, the leo.yaml it was loaded from. A follower's empty
// local keys count on both sides.
func sameAsLoaded(path string, leoBytes []byte, view *Config, data []byte) (bool, error) {
	loaded, err := parse(path, leoBytes)
	if err != nil {
		return false, err
	}
	if view.follow == nil || view.follow.cache == nil {
		return sameYAML(loaded, view)
	}
	loaded.localEmpties = fileEmpties(leoBytes)
	was, err := marshalLocal(loaded)
	if err != nil {
		return false, fmt.Errorf("marshaling for comparison: %w", err)
	}
	return sameDocument(was, data)
}

// isSharedSource reports whether a template source lives in the
// templates_file.
func isSharedSource(s TemplateSource) bool {
	return s == SourceShared || s == SourceSharedOverrideLead
}
