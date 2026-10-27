package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// toJSONValue encodes v through yaml (so its yaml tags, omitempty rules and
// custom marshalers apply, e.g. RoleTarget's shorthand) and converts the
// resulting node to plain JSON values that keep their YAML types.
func toJSONValue(v any) (any, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return nodeToJSON(&n)
}

// nodeToJSON converts a YAML node to JSON values: mappings become
// map[string]any, sequences []any, and scalars keep their resolved type
// (int64, float64 as json.Number, bool, nil, string), so an int crosses as an
// int and the string "123" stays a string.
func nodeToJSON(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return nodeToJSON(n.Content[0])
	case yaml.AliasNode:
		return nodeToJSON(n.Alias)
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: mapping key must be a scalar", key.Line)
			}
			v, err := nodeToJSON(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[key.Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := nodeToJSON(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		return scalarToJSON(n)
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node kind %d", n.Line, n.Kind)
}

func scalarToJSON(n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		err := n.Decode(&b)
		return b, err
	case "!!int":
		var i int64
		err := n.Decode(&i)
		return i, err
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil {
			return nil, err
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("line %d: %s has no JSON form", n.Line, n.Value)
		}
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0" // keep it a float on the way back
		}
		return json.Number(s), nil
	}
	return n.Value, nil
}

// jsonToNode is nodeToJSON's inverse for values decoded with UseNumber: a
// json.Number with a fraction or exponent becomes a !!float, any other an
// !!int. Mapping keys are sorted so the result is deterministic.
func jsonToNode(v any) (*yaml.Node, error) {
	switch t := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}, nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(t.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: t.String()}, nil
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range slices.Sorted(maps.Keys(t)) {
			val, err := jsonToNode(t[k])
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, val)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range t {
			val, err := jsonToNode(e)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, val)
		}
		return n, nil
	}
	return nil, fmt.Errorf("unsupported JSON value %T", v)
}

// decodeJSONValue decodes a UseNumber JSON value into out through YAML, so
// out's yaml tags and custom unmarshalers apply exactly as when loading
// leo.yaml.
func decodeJSONValue(v any, out any) error {
	n, err := jsonToNode(v)
	if err != nil {
		return err
	}
	return n.Decode(out)
}
