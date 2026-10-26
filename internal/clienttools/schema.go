package clienttools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
)

// Schema limits: a tool's input_schema is forwarded to every agent's harness,
// so it must stay small and shallow.
const (
	// MaxSchemaDepth is the deepest schema nesting accepted; the root is 0.
	MaxSchemaDepth = 16
	// MaxSchemaNodes is the most schema objects (root included) one tool's
	// input_schema may contain.
	MaxSchemaNodes = 512
)

var schemaPrimitiveTypes = map[string]bool{
	"string": true, "number": true, "integer": true, "boolean": true,
	"object": true, "array": true, "null": true,
}

// schemaKeywords is the supported subset of JSON Schema 2020-12 — the
// keywords MCP tool schemas use — each with the check for its value. Unknown
// keywords are rejected rather than ignored: a keyword the daemon does not
// understand (a dangling $ref, a draft-07 tuple `items`) is exactly what can
// make a harness refuse the whole tool list, and it cannot tell which
// harness will. Annotations that never affect validation are listed here too.
var schemaKeywords map[string]func(v any, c *schemaCheck, path string, depth int) error

func init() {
	schemaKeywords = map[string]func(any, *schemaCheck, string, int) error{
		"type":                 checkSchemaType,
		"properties":           checkSchemaMap,
		"required":             checkRequired,
		"items":                checkSubschema,
		"additionalProperties": checkAdditionalProperties,
		"anyOf":                checkSubschemaList,
		"oneOf":                checkSubschemaList,
		"allOf":                checkSubschemaList,
		"enum":                 checkEnum,
		"const":                checkAny,
		"default":              checkAny,
		"examples":             checkArray,
		"description":          checkString,
		"title":                checkString,
		"format":               checkString,
		"pattern":              checkString,
		"$schema":              checkString,
		"$comment":             checkString,
		"minimum":              checkNumber,
		"maximum":              checkNumber,
		"exclusiveMinimum":     checkNumber,
		"exclusiveMaximum":     checkNumber,
		"multipleOf":           checkPositiveNumber,
		"minLength":            checkCount,
		"maxLength":            checkCount,
		"minItems":             checkCount,
		"maxItems":             checkCount,
		"minProperties":        checkCount,
		"maxProperties":        checkCount,
		"uniqueItems":          checkBool,
		"deprecated":           checkBool,
		"readOnly":             checkBool,
		"writeOnly":            checkBool,
	}
}

// schemaCheck carries the per-schema node budget through the walk.
type schemaCheck struct{ nodes int }

// validateSchema checks a tool's input_schema against the supported subset of
// JSON Schema: the root is an object schema, every nested schema (properties,
// items, additionalProperties, anyOf/oneOf/allOf) is checked the same way, to
// MaxSchemaDepth levels and MaxSchemaNodes schemas. Errors name the JSON path
// of the offending value, e.g. `at $.properties.ref.type: unknown type "x"`.
func validateSchema(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("must be a JSON Schema object: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("must be a single JSON value")
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return fmt.Errorf("must be a JSON Schema object")
	}
	if root["type"] != "object" {
		return fmt.Errorf(`must have "type": "object"`)
	}
	return checkSchema(root, &schemaCheck{}, "$", 0)
}

func checkSchema(v any, c *schemaCheck, path string, depth int) error {
	s, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("at %s: a schema must be an object", path)
	}
	if depth > MaxSchemaDepth {
		return fmt.Errorf("at %s: schema nests deeper than %d levels", path, MaxSchemaDepth)
	}
	if c.nodes++; c.nodes > MaxSchemaNodes {
		return fmt.Errorf("at %s: schema has more than %d subschemas", path, MaxSchemaNodes)
	}
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		check, known := schemaKeywords[k]
		if !known {
			return fmt.Errorf("at %s.%s: unsupported keyword %q", path, k, k)
		}
		if err := check(s[k], c, path+"."+k, depth); err != nil {
			return err
		}
	}
	return nil
}

func checkSchemaType(v any, _ *schemaCheck, path string, _ int) error {
	switch t := v.(type) {
	case string:
		if !schemaPrimitiveTypes[t] {
			return fmt.Errorf("at %s: unknown type %q", path, t)
		}
		return nil
	case []any:
		if len(t) == 0 {
			return fmt.Errorf("at %s: type list must not be empty", path)
		}
		seen := map[string]bool{}
		for i, e := range t {
			name, ok := e.(string)
			if !ok || !schemaPrimitiveTypes[name] {
				return fmt.Errorf("at %s[%d]: unknown type %v", path, i, e)
			}
			if seen[name] {
				return fmt.Errorf("at %s[%d]: duplicate type %q", path, i, name)
			}
			seen[name] = true
		}
		return nil
	}
	return fmt.Errorf("at %s: type must be a string or a list of strings", path)
}

func checkSchemaMap(v any, c *schemaCheck, path string, depth int) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("at %s: must be an object of schemas", path)
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := checkSchema(m[name], c, path+"."+name, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func checkRequired(v any, _ *schemaCheck, path string, _ int) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("at %s: must be a list of property names", path)
	}
	seen := map[string]bool{}
	for i, e := range list {
		name, ok := e.(string)
		if !ok {
			return fmt.Errorf("at %s[%d]: must be a string", path, i)
		}
		if seen[name] {
			return fmt.Errorf("at %s[%d]: duplicate property %q", path, i, name)
		}
		seen[name] = true
	}
	return nil
}

func checkSubschema(v any, c *schemaCheck, path string, depth int) error {
	return checkSchema(v, c, path, depth+1)
}

func checkAdditionalProperties(v any, c *schemaCheck, path string, depth int) error {
	if _, ok := v.(bool); ok {
		return nil
	}
	return checkSchema(v, c, path, depth+1)
}

func checkSubschemaList(v any, c *schemaCheck, path string, depth int) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return fmt.Errorf("at %s: must be a non-empty list of schemas", path)
	}
	for i, e := range list {
		if err := checkSchema(e, c, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func checkEnum(v any, _ *schemaCheck, path string, _ int) error {
	if list, ok := v.([]any); !ok || len(list) == 0 {
		return fmt.Errorf("at %s: must be a non-empty list", path)
	}
	return nil
}

func checkAny(any, *schemaCheck, string, int) error { return nil }

func checkArray(v any, _ *schemaCheck, path string, _ int) error {
	if _, ok := v.([]any); !ok {
		return fmt.Errorf("at %s: must be a list", path)
	}
	return nil
}

func checkString(v any, _ *schemaCheck, path string, _ int) error {
	if _, ok := v.(string); !ok {
		return fmt.Errorf("at %s: must be a string", path)
	}
	return nil
}

func checkBool(v any, _ *schemaCheck, path string, _ int) error {
	if _, ok := v.(bool); !ok {
		return fmt.Errorf("at %s: must be true or false", path)
	}
	return nil
}

func checkNumber(v any, _ *schemaCheck, path string, _ int) error {
	if _, ok := v.(json.Number); !ok {
		return fmt.Errorf("at %s: must be a number", path)
	}
	return nil
}

func checkPositiveNumber(v any, c *schemaCheck, path string, d int) error {
	if err := checkNumber(v, c, path, d); err != nil {
		return err
	}
	if f, err := v.(json.Number).Float64(); err != nil || f <= 0 || math.IsInf(f, 0) {
		return fmt.Errorf("at %s: must be greater than 0", path)
	}
	return nil
}

func checkCount(v any, _ *schemaCheck, path string, _ int) error {
	n, ok := v.(json.Number)
	if !ok {
		return fmt.Errorf("at %s: must be a non-negative integer", path)
	}
	if i, err := n.Int64(); err != nil || i < 0 {
		return fmt.Errorf("at %s: must be a non-negative integer", path)
	}
	return nil
}
