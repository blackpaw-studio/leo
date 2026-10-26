package clienttools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValidateSchema(t *testing.T) {
	nested := func(depth int) string {
		s := `{"type":"string"}`
		for i := 0; i < depth; i++ {
			s = `{"type":"array","items":` + s + `}`
		}
		return `{"type":"object","properties":{"p":` + s + `}}`
	}
	tests := []struct {
		name    string
		schema  string
		wantErr string // substring; empty means valid
	}{
		{name: "mcp style", schema: `{"type":"object","properties":{"ref":{"type":"string","description":"e1"},"tab":{"type":"integer","minimum":0,"maximum":9},"mode":{"enum":["a","b"],"default":"a"}},"required":["ref"],"additionalProperties":false}`},
		{name: "type list", schema: `{"type":"object","properties":{"v":{"type":["string","null"]}}}`},
		{name: "items and combinators", schema: `{"type":"object","properties":{"l":{"type":"array","items":{"anyOf":[{"type":"string","pattern":"^a","maxLength":3},{"type":"number","exclusiveMinimum":0}]},"minItems":1,"uniqueItems":true}}}`},
		{name: "additionalProperties schema", schema: `{"type":"object","additionalProperties":{"type":"string"}}`},
		{name: "annotations", schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","title":"t","type":"object","examples":[{}]}`},

		{name: "nested unknown type", schema: `{"type":"object","properties":{"ref":{"type":"bogus"}}}`, wantErr: `at $.properties.ref.type: unknown type "bogus"`},
		{name: "unknown type in list", schema: `{"type":"object","properties":{"ref":{"type":["string","bogus"]}}}`, wantErr: `at $.properties.ref.type[1]`},
		{name: "empty type list", schema: `{"type":"object","properties":{"ref":{"type":[]}}}`, wantErr: `at $.properties.ref.type`},
		{name: "type wrong kind", schema: `{"type":"object","properties":{"ref":{"type":7}}}`, wantErr: `at $.properties.ref.type`},
		{name: "property not a schema", schema: `{"type":"object","properties":{"ref":"string"}}`, wantErr: `at $.properties.ref: a schema must be an object`},
		{name: "items unknown type", schema: `{"type":"object","properties":{"l":{"type":"array","items":{"type":"nope"}}}}`, wantErr: `at $.properties.l.items.type`},
		{name: "tuple items rejected", schema: `{"type":"object","properties":{"l":{"type":"array","items":[{"type":"string"}]}}}`, wantErr: `at $.properties.l.items`},
		{name: "anyOf bad member", schema: `{"type":"object","properties":{"v":{"anyOf":[{"type":"string"},{"type":"zzz"}]}}}`, wantErr: `at $.properties.v.anyOf[1].type`},
		{name: "anyOf empty", schema: `{"type":"object","properties":{"v":{"anyOf":[]}}}`, wantErr: `at $.properties.v.anyOf`},
		{name: "additionalProperties wrong kind", schema: `{"type":"object","additionalProperties":"no"}`, wantErr: `at $.additionalProperties`},
		{name: "nested required not strings", schema: `{"type":"object","properties":{"o":{"type":"object","required":[1]}}}`, wantErr: `at $.properties.o.required[0]`},
		{name: "enum empty", schema: `{"type":"object","properties":{"v":{"enum":[]}}}`, wantErr: `at $.properties.v.enum`},
		{name: "description not a string", schema: `{"type":"object","properties":{"v":{"description":5}}}`, wantErr: `at $.properties.v.description`},
		{name: "minimum not a number", schema: `{"type":"object","properties":{"v":{"minimum":"1"}}}`, wantErr: `at $.properties.v.minimum`},
		{name: "minLength negative", schema: `{"type":"object","properties":{"v":{"minLength":-1}}}`, wantErr: `at $.properties.v.minLength`},
		{name: "minItems fractional", schema: `{"type":"object","properties":{"v":{"minItems":1.5}}}`, wantErr: `at $.properties.v.minItems`},
		{name: "pattern not a string", schema: `{"type":"object","properties":{"v":{"pattern":1}}}`, wantErr: `at $.properties.v.pattern`},
		{name: "dangling ref", schema: `{"type":"object","properties":{"v":{"$ref":"#/$defs/x"}}}`, wantErr: `unsupported keyword "$ref"`},
		{name: "unknown keyword at root", schema: `{"type":"object","patternProperties":{}}`, wantErr: `unsupported keyword "patternProperties"`},
		{name: "depth at limit", schema: nested(MaxSchemaDepth/2 - 1)},
		{name: "too deep", schema: nested(MaxSchemaDepth), wantErr: "deeper than"},
		{name: "too many subschemas", schema: func() string {
			props := make([]string, 0, MaxSchemaNodes)
			for i := 0; i < MaxSchemaNodes; i++ {
				props = append(props, fmt.Sprintf(`"p%d":{"type":"string"}`, i))
			}
			return `{"type":"object","properties":{` + strings.Join(props, ",") + `}}`
		}(), wantErr: "more than"},
		{name: "trailing data", schema: `{"type":"object"} {}`, wantErr: "single JSON value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSchema(json.RawMessage(tc.schema))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want valid", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRegisterRejectsANestedBadSchema(t *testing.T) {
	r := newTestRegistry()
	id := r.Connect()
	bad := Tool{Name: "t", InputSchema: json.RawMessage(`{"type":"object","properties":{"ref":{"type":"bogus"}}}`)}
	_, _, err := r.Register(id, "g", []Tool{bad})
	if got := registerCode(t, err); got != CodeInvalidTool {
		t.Fatalf("code = %q (%v)", got, err)
	}
	if !strings.Contains(err.Error(), "$.properties.ref.type") {
		t.Errorf("error does not name the path: %v", err)
	}
	if snap := r.Snapshot(); len(snap.Tools) != 0 {
		t.Errorf("bad tool was registered: %+v", snap.Tools)
	}
}
