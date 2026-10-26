package clienttools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const objectSchema = `{"type":"object","properties":{"ref":{"type":"string"}},"required":["ref"]}`

func tool(name string) Tool {
	return Tool{Name: name, Description: "does " + name, InputSchema: json.RawMessage(objectSchema), TimeoutS: 30}
}

func newTestRegistry() *Registry {
	n := 0
	return NewRegistry(RegistryOptions{
		IsBuiltin: func(name string) bool { return name == "leo_chat_post" },
		NewClientID: func() ClientID {
			n++
			return ClientID(fmt.Sprintf("ct_%026d", n))
		},
	})
}

func registerCode(t *testing.T, err error) string {
	t.Helper()
	var re *RegisterError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RegisterError", err)
	}
	return re.Code
}

func TestRegisterValidation(t *testing.T) {
	manyTools := func(n int) []Tool {
		out := make([]Tool, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, tool(fmt.Sprintf("t%d", i)))
		}
		return out
	}
	badSchema := func(raw string) Tool {
		return Tool{Name: "ok_tool", Description: "d", InputSchema: json.RawMessage(raw)}
	}

	tests := []struct {
		name     string
		setup    func(r *Registry, other ClientID)
		group    string
		tools    []Tool
		wantCode string
		wantTool string
	}{
		{name: "valid", group: "g", tools: []Tool{tool("browser_click")}},
		{name: "uppercase name", group: "g", tools: []Tool{tool("Bad Name")}, wantCode: CodeInvalidTool},
		{name: "leading digit", group: "g", tools: []Tool{tool("1tool")}, wantCode: CodeInvalidTool},
		{name: "name too long", group: "g", tools: []Tool{tool("a" + strings.Repeat("b", 64))}, wantCode: CodeInvalidTool},
		{name: "name at limit", group: "g", tools: []Tool{tool("a" + strings.Repeat("b", 63))}},
		{name: "duplicate in request", group: "g", tools: []Tool{tool("dup"), tool("dup")}, wantCode: CodeInvalidTool},
		{name: "negative timeout", group: "g", tools: []Tool{{Name: "neg", InputSchema: json.RawMessage(objectSchema), TimeoutS: -1}}, wantCode: CodeInvalidTool},
		{name: "empty group", group: "", tools: []Tool{tool("a")}, wantCode: CodeInvalidTool},
		{name: "builtin collision", group: "g", tools: []Tool{tool("leo_chat_post")}, wantCode: CodeNameConflict, wantTool: "leo_chat_post"},
		{
			name: "other group collision",
			setup: func(r *Registry, other ClientID) {
				if _, _, err := r.Register(other, "first", []Tool{tool("shared")}); err != nil {
					panic(err)
				}
			},
			group: "second", tools: []Tool{tool("shared")}, wantCode: CodeNameConflict, wantTool: "shared",
		},
		{name: "65th tool", group: "g", tools: manyTools(65), wantCode: CodeTooManyTools},
		{name: "64 tools", group: "g", tools: manyTools(64)},
		{name: "schema not json", group: "g", tools: []Tool{badSchema(`nope`)}, wantCode: CodeInvalidTool},
		{name: "schema missing", group: "g", tools: []Tool{badSchema(``)}, wantCode: CodeInvalidTool},
		{name: "schema array", group: "g", tools: []Tool{badSchema(`[]`)}, wantCode: CodeInvalidTool},
		{name: "schema wrong type", group: "g", tools: []Tool{badSchema(`{"type":"string"}`)}, wantCode: CodeInvalidTool},
		{name: "schema properties not object", group: "g", tools: []Tool{badSchema(`{"type":"object","properties":[]}`)}, wantCode: CodeInvalidTool},
		{name: "schema required not strings", group: "g", tools: []Tool{badSchema(`{"type":"object","required":[1]}`)}, wantCode: CodeInvalidTool},
		{name: "schema no properties ok", group: "g", tools: []Tool{badSchema(`{"type":"object"}`)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRegistry()
			id, other := r.Connect(), r.Connect()
			if tc.setup != nil {
				tc.setup(r, other)
			}
			_, _, err := r.Register(id, tc.group, tc.tools)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("Register: %v", err)
				}
				return
			}
			if got := registerCode(t, err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (%v)", got, tc.wantCode, err)
			}
			var re *RegisterError
			errors.As(err, &re)
			if re.Tool != tc.wantTool {
				t.Errorf("tool = %q, want %q", re.Tool, tc.wantTool)
			}
		})
	}
}

func TestRegisterUnknownClient(t *testing.T) {
	r := newTestRegistry()
	_, _, err := r.Register("ct_nope", "g", []Tool{tool("a")})
	if got := registerCode(t, err); got != CodeUnknownClient {
		t.Fatalf("code = %q", got)
	}
}

func TestPerDaemonToolCap(t *testing.T) {
	r := newTestRegistry()
	a, b := r.Connect(), r.Connect()
	first := make([]Tool, 0, 40)
	second := make([]Tool, 0, 30)
	for i := 0; i < 40; i++ {
		first = append(first, tool(fmt.Sprintf("a%d", i)))
	}
	for i := 0; i < 30; i++ {
		second = append(second, tool(fmt.Sprintf("b%d", i)))
	}
	if _, _, err := r.Register(a, "ga", first); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.Register(b, "gb", second)
	if got := registerCode(t, err); got != CodeTooManyTools {
		t.Fatalf("code = %q, want too_many_tools", got)
	}
}

func TestRegisterBumpsRevisionOnlyOnChange(t *testing.T) {
	r := newTestRegistry()
	a := r.Connect()
	rev1, changed, err := r.Register(a, "g", []Tool{tool("x")})
	if err != nil || !changed || rev1 == 0 {
		t.Fatalf("first: rev=%d changed=%v err=%v", rev1, changed, err)
	}
	rev2, changed, err := r.Register(a, "g", []Tool{tool("x")})
	if err != nil || changed || rev2 != rev1 {
		t.Fatalf("identical re-post: rev=%d changed=%v err=%v", rev2, changed, err)
	}
	rev3, changed, err := r.Register(a, "g", []Tool{tool("x"), tool("y")})
	if err != nil || !changed || rev3 <= rev2 {
		t.Fatalf("replace: rev=%d changed=%v err=%v", rev3, changed, err)
	}
	if got := toolNames(r.Snapshot()); strings.Join(got, ",") != "x,y" {
		t.Fatalf("tools = %v", got)
	}
}

func TestIdenticalGroupSharesProviders(t *testing.T) {
	r := newTestRegistry()
	a, b := r.Connect(), r.Connect()
	revA, _, err := r.Register(a, "browser", []Tool{tool("browser_click")})
	if err != nil {
		t.Fatal(err)
	}
	// Same definitions, different key order in the schema: still identical.
	reordered := tool("browser_click")
	reordered.InputSchema = json.RawMessage(`{"required":["ref"],"properties":{"ref":{"type":"string"}},"type":"object"}`)
	revB, changed, err := r.Register(b, "browser", []Tool{reordered})
	if err != nil {
		t.Fatal(err)
	}
	if changed || revB != revA {
		t.Fatalf("second provider bumped revision: %d -> %d changed=%v", revA, revB, changed)
	}
	snap := r.Snapshot()
	if len(snap.Tools) != 1 {
		t.Fatalf("tools listed %d times, want once: %+v", len(snap.Tools), snap.Tools)
	}
	if got := r.Providers("browser"); len(got) != 2 {
		t.Fatalf("providers = %v", got)
	}

	// Different definitions from another client into the same group conflict.
	c := r.Connect()
	different := tool("browser_click")
	different.Description = "something else"
	_, _, err = r.Register(c, "browser", []Tool{different})
	if got := registerCode(t, err); got != CodeNameConflict {
		t.Fatalf("code = %q", got)
	}
}

func TestDisconnectWithdrawsAndBumpsRevision(t *testing.T) {
	r := newTestRegistry()
	a, b := r.Connect(), r.Connect()
	if _, _, err := r.Register(a, "browser", []Tool{tool("browser_click")}); err != nil {
		t.Fatal(err)
	}
	rev, _, err := r.Register(b, "browser", []Tool{tool("browser_click")})
	if err != nil {
		t.Fatal(err)
	}

	if _, changed := r.Disconnect(a); changed {
		t.Fatal("tools withdrawn while another provider remains")
	}
	if len(r.Snapshot().Tools) != 1 {
		t.Fatal("tool vanished with a provider still connected")
	}
	got, changed := r.Disconnect(b)
	if !changed || got <= rev {
		t.Fatalf("last provider gone: rev=%d changed=%v (was %d)", got, changed, rev)
	}
	if len(r.Snapshot().Tools) != 0 {
		t.Fatal("tools survive their last provider")
	}
	// A disconnected client can no longer register.
	if _, _, err := r.Register(a, "browser", []Tool{tool("browser_click")}); registerCode(t, err) != CodeUnknownClient {
		t.Fatalf("register after disconnect: %v", err)
	}
	// Disconnecting twice is harmless.
	if _, changed := r.Disconnect(a); changed {
		t.Fatal("second disconnect reported a change")
	}
}

func TestLookup(t *testing.T) {
	r := newTestRegistry()
	a := r.Connect()
	if _, _, err := r.Register(a, "browser", []Tool{tool("browser_click")}); err != nil {
		t.Fatal(err)
	}
	group, got, ok := r.Lookup("browser_click")
	if !ok || group != "browser" || got.TimeoutS != 30 {
		t.Fatalf("Lookup = %q %+v %v", group, got, ok)
	}
	if _, _, ok := r.Lookup("nope"); ok {
		t.Fatal("Lookup found an unregistered tool")
	}
	if !r.Has("browser_click") || r.Has("nope") {
		t.Fatal("Has disagrees with Lookup")
	}
}

func toolNames(s Snapshot) []string {
	out := make([]string, 0, len(s.Tools))
	for _, tl := range s.Tools {
		out = append(out, tl.Name)
	}
	return out
}
