package config

import (
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

// A defaults or delegation key present in a follower's leo.yaml overrides
// the lead even when its value is empty, but omitempty drops most empty
// values when Save marshals the structs. The helpers here carry such keys
// through a save: localView finds them (local empties, and lead keys the
// follower cleared) and marshalLocal writes them back.

// Keys Save writes even when unset, because their yaml tags lack omitempty.
// An empty local value for one of these counts as unset, so a stale
// `model: ""` cannot hide the lead's model.
var (
	defaultsUnsetWhenEmpty   = map[string]bool{"model": true, "max_turns": true}
	delegationUnsetWhenEmpty = map[string]bool{"active_profile": true}
)

// localEmpties holds a follower local view's empty keys, per section.
type localEmpties struct {
	defaults, delegation *yaml.Node
}

func newLocalEmpties(defaults, delegation *yaml.Node) *localEmpties {
	if defaults == nil && delegation == nil {
		return nil
	}
	return &localEmpties{defaults: defaults, delegation: delegation}
}

// fileEmpties returns the empty keys leo.yaml sets in defaults and
// delegation.
func fileEmpties(leoBytes []byte) *localEmpties {
	defaults, delegation := localSectionNodes(leoBytes)
	return newLocalEmpties(presentEmpties(defaults, followDeepKeys), presentEmpties(withoutKey(delegation, "profiles"), nil))
}

// marshalLocal marshals c, writing back the empty keys of its local view.
func marshalLocal(c *Config) ([]byte, error) {
	if c.localEmpties == nil {
		return yaml.Marshal(c)
	}
	var doc yaml.Node
	if err := doc.Encode(c); err != nil {
		return nil, err
	}
	addSectionEmpties(&doc, "defaults", c.localEmpties.defaults)
	addSectionEmpties(&doc, "delegation", c.localEmpties.delegation)
	return yaml.Marshal(&doc)
}

func addSectionEmpties(doc *yaml.Node, section string, empties *yaml.Node) {
	if empties == nil {
		return
	}
	if v := mappingValue(doc, section); v != nil {
		addAbsent(v, empties)
		return
	}
	doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: section}, empties)
}

// sameDocument reports whether two YAML documents hold the same data,
// ignoring key order and formatting.
func sameDocument(a, b []byte) (bool, error) {
	var av, bv any
	if err := yaml.Unmarshal(a, &av); err != nil {
		return false, fmt.Errorf("parsing for comparison: %w", err)
	}
	if err := yaml.Unmarshal(b, &bv); err != nil {
		return false, fmt.Errorf("parsing for comparison: %w", err)
	}
	return reflect.DeepEqual(av, bv), nil
}

// presentEmpties returns the keys of mapping n whose value is an empty
// sequence, mapping or string, one level deeper for deep keys; nil when
// there are none.
func presentEmpties(n *yaml.Node, deep map[string]bool) *yaml.Node {
	if n = resolveAlias(n); n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], resolveAlias(n.Content[i+1])
		if deep[key.Value] && val.Kind == yaml.MappingNode && len(val.Content) > 0 {
			if sub := presentEmpties(val, nil); sub != nil {
				out.Content = append(out.Content, key, sub)
			}
			continue
		}
		if e := emptyLike(val); e != nil && isEmptyNode(val) {
			out.Content = append(out.Content, key, e)
		}
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}

// emptyLike returns an empty value of n's shape; nil for a number, bool or
// null, whose fields are pointers where unset already means "inherit".
func emptyLike(n *yaml.Node) *yaml.Node {
	switch n = resolveAlias(n); {
	case n == nil:
		return nil
	case n.Kind == yaml.SequenceNode:
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	case n.Kind == yaml.MappingNode:
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	case n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str":
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ""}
	}
	return nil
}

// addAbsent adds to mapping dst each key of src it lacks, recursing where
// both values are mappings.
func addAbsent(dst, src *yaml.Node) {
	if dst = resolveAlias(dst); dst == nil || dst.Kind != yaml.MappingNode {
		return
	}
	if src = resolveAlias(src); src == nil || src.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, val := src.Content[i], src.Content[i+1]
		have := resolveAlias(mappingValue(dst, key.Value))
		switch {
		case have == nil:
			dst.Content = append(dst.Content, key, val)
		case have.Kind == yaml.MappingNode && resolveAlias(val).Kind == yaml.MappingNode:
			addAbsent(have, val)
		}
	}
}
