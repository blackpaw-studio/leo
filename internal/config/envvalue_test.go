package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

func TestEnvValueDecodeYAML(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want EnvValue
	}{
		{"string", `v: hello`, Literal("hello")},
		{"number keeps its text", `v: 0x1F`, Literal("0x1F")},
		{"float keeps its text", `v: 1.50`, Literal("1.50")},
		{"bool", `v: true`, Literal("true")},
		{"null", `v:`, Literal("")},
		{"tilde null", `v: ~`, Literal("")},
		{"alias", "a: &x hi\nv: *x", Literal("hi")},
		{"keychain ref", "v:\n  secret: keychain\n  service: S\n  account: evan", Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: "S", Account: "evan"})},
		{"flow ref", `v: {secret: secret-service, attributes: {service: leo, key: A}}`, Secret(secretenv.Ref{Backend: secretenv.SecretService, Attributes: map[string]string{"service": "leo", "key": "A"}})},
		{"command ref", `v: {secret: command, argv: [pass, show, leo/d]}`, Secret(secretenv.Ref{Backend: secretenv.Command, Argv: []string{"pass", "show", "leo/d"}})},
		{"unknown keys are kept for Validate", `v: {secret: file, path: /x, mode: 1, zz: 2}`, Secret(secretenv.Ref{Backend: secretenv.File, Path: "/x", Unknown: []string{"mode", "zz"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]EnvValue
			if err := yaml.Unmarshal([]byte(tc.src), &m); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(m["v"], tc.want) {
				t.Errorf("got %#v, want %#v", m["v"], tc.want)
			}
		})
	}
}

func TestEnvValueDecodeYAMLRejectsSequence(t *testing.T) {
	var m map[string]EnvValue
	err := yaml.Unmarshal([]byte("v: [a, b]"), &m)
	if err == nil || !strings.Contains(err.Error(), "env value must be a string or a secret reference") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvValueDecodeJSON(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want EnvValue
	}{
		{"string", `"hello"`, Literal("hello")},
		{"null", `null`, Literal("")},
		{"number", `8080`, Literal("8080")},
		{"bool", `true`, Literal("true")},
		{"ref", `{"secret":"keychain","service":"S"}`, Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: "S"})},
		{"ref with unknown keys", `{"secret":"file","path":"/x","zz":1}`, Secret(secretenv.Ref{Backend: secretenv.File, Path: "/x", Unknown: []string{"zz"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v EnvValue
			if err := json.Unmarshal([]byte(tc.src), &v); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(v, tc.want) {
				t.Errorf("got %#v, want %#v", v, tc.want)
			}
		})
	}
	var v EnvValue
	if err := json.Unmarshal([]byte(`["a"]`), &v); err == nil {
		t.Error("an array decoded without error")
	}
}

func TestEnvValueRoundTrip(t *testing.T) {
	env := EnvMap{
		"LIT": Literal("plain"),
		"REF": Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: "S", Account: "evan"}),
	}
	y, err := yaml.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if want := "LIT: plain\nREF:\n    secret: keychain\n    service: S\n    account: evan\n"; string(y) != want {
		t.Errorf("yaml = %q, want %q", y, want)
	}
	var back EnvMap
	if err := yaml.Unmarshal(y, &back); err != nil || !reflect.DeepEqual(back, env) {
		t.Errorf("yaml round trip = %#v, %v", back, err)
	}
	j, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"LIT":"plain","REF":{"secret":"keychain","service":"S","account":"evan"}}`; string(j) != want {
		t.Errorf("json = %s, want %s", j, want)
	}
	back = nil
	if err := json.Unmarshal(j, &back); err != nil || !reflect.DeepEqual(back, env) {
		t.Errorf("json round trip = %#v, %v", back, err)
	}
}

func TestEnvMapViews(t *testing.T) {
	ref := secretenv.Ref{Backend: secretenv.File, Path: "/x"}
	env := EnvMap{"A": Literal("1"), "B": Secret(ref)}
	if got := env.Keys(); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("Keys = %v", got)
	}
	if got := env.Literals(); !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Errorf("Literals = %v", got)
	}
	if got := env.Refs(); !reflect.DeepEqual(got, map[string]secretenv.Ref{"B": ref}) {
		t.Errorf("Refs = %v", got)
	}
	if got := LiteralEnv(map[string]string{"A": "1"}); !reflect.DeepEqual(got, EnvMap{"A": Literal("1")}) {
		t.Errorf("LiteralEnv = %v", got)
	}
	if LiteralEnv(nil) != nil {
		t.Error("LiteralEnv(nil) is not nil")
	}
}

func TestValidateRejectsBadSecretRefs(t *testing.T) {
	cfg := &Config{
		Environments: map[string]EnvMap{
			"acct": {"TOK": Secret(secretenv.Ref{Backend: "vault"})},
		},
		Templates: map[string]TemplateConfig{
			"coder": {Env: EnvMap{"TOK": Secret(secretenv.Ref{Backend: secretenv.Keychain})}},
		},
		Tasks: map[string]TaskConfig{
			"nightly": {Schedule: "0 3 * * *", PromptFile: "p.md", Env: EnvMap{"TOK": Secret(secretenv.Ref{Backend: secretenv.Command})}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted bad refs")
	}
	for _, want := range []string{
		`environments.acct.TOK: secret: unknown backend "vault"`,
		"templates.coder.env.TOK: secret: keychain requires service",
		"tasks.nightly.env.TOK: secret: command requires argv",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestExpandFailsClosedOnRefBeforeResolution(t *testing.T) {
	ref := secretenv.Ref{Backend: secretenv.Keychain, Service: "S"}
	_, err := ExpandLiteral(EnvMap{"TOK": Secret(ref)}, Expander{})
	var se *SecretResolutionError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *SecretResolutionError", err, err)
	}
	if se.Key != "TOK" || se.Ref.Service != "S" {
		t.Errorf("err = %#v", se)
	}
}
