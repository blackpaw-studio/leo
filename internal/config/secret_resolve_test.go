package config

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const secretSentinel = "S3CR3T-SENTINEL"

// fakeSecrets resolves every reference to value (or fails with reason) and
// records each call.
type fakeSecrets struct {
	mu     sync.Mutex
	calls  []secretenv.Ref
	value  string
	reason string
}

func (f *fakeSecrets) Resolve(r secretenv.Ref) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	if f.reason != "" {
		return "", &secretenv.ResolutionError{Ref: r, Reason: f.reason}
	}
	return f.value, nil
}

func secretExpander(f *fakeSecrets) Expander {
	x := fakeExpander(map[string]string{"HOME": "/home/evan"})
	x.Secrets = f
	return x
}

var keychainRef = secretenv.Ref{Backend: secretenv.Keychain, Service: "OP"}

func TestResolveEnvResolvesNamedAndLiteralRefs(t *testing.T) {
	c := &Config{Environments: map[string]EnvMap{
		"acct": {"A_TOKEN": Secret(keychainRef), "PLAIN": Literal("$HOME/x")},
	}}
	f := &fakeSecrets{value: secretSentinel}
	got, err := c.ResolveEnv([]string{"acct"}, EnvMap{"B_TOKEN": Secret(keychainRef)}, nil, secretExpander(f))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A_TOKEN": secretSentinel, "B_TOKEN": secretSentinel, "PLAIN": "/home/evan/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

func TestResolvedSecretIsNeverExpanded(t *testing.T) {
	f := &fakeSecrets{value: "$HOME/${NOPE}~"}
	got, err := ExpandLiteral(EnvMap{"K": Secret(keychainRef)}, secretExpander(f))
	if err != nil || got["K"] != "$HOME/${NOPE}~" {
		t.Errorf("got %q, %v; want the value verbatim", got["K"], err)
	}
}

func TestResolveEnvSecretFailureNamesScopeKeyAndRef(t *testing.T) {
	c := &Config{Environments: map[string]EnvMap{"acct": {"A_TOKEN": Secret(keychainRef)}}}
	f := &fakeSecrets{reason: "item not found"}
	_, err := c.ResolveEnv([]string{"acct"}, nil, nil, secretExpander(f))
	var se *SecretResolutionError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T)", err, err)
	}
	if se.Scope != "environments.acct" || se.Key != "A_TOKEN" || se.Ref.Service != "OP" || se.Reason != "item not found" {
		t.Errorf("err = %#v", se)
	}
	if want := "environments.acct.A_TOKEN: secret keychain(service=OP): item not found"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err, want)
	}
}

func TestSecretErrorsNeverCarryTheValue(t *testing.T) {
	// A resolver whose error text carries the value must not leak it: only
	// the reason of a *secretenv.ResolutionError is kept.
	leaky := resolverFunc(func(secretenv.Ref) (string, error) {
		return "", errors.New("boom " + secretSentinel)
	})
	x := fakeExpander(nil)
	x.Secrets = leaky
	_, err := ExpandLiteral(EnvMap{"K": Secret(keychainRef)}, x)
	if err == nil || strings.Contains(err.Error(), secretSentinel) {
		t.Errorf("err = %v", err)
	}
}

type resolverFunc func(secretenv.Ref) (string, error)

func (f resolverFunc) Resolve(r secretenv.Ref) (string, error) { return f(r) }

func TestFileRefPathIsExpandedBeforeResolve(t *testing.T) {
	f := &fakeSecrets{value: "v"}
	ref := secretenv.Ref{Backend: secretenv.File, Path: "~/.config/$APP/tok"}
	x := secretExpander(f)
	x.Lookup = func(k string) (string, bool) { return map[string]string{"APP": "leo"}[k], k == "APP" }
	if _, err := ExpandLiteral(EnvMap{"K": Secret(ref)}, x); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].Path != "/home/evan/.config/leo/tok" {
		t.Errorf("resolved %v", f.calls)
	}
}

func TestShadowedRefIsNeverResolved(t *testing.T) {
	// A lower layer's ref whose backend would fail here, overridden by a
	// higher layer: the launch succeeds without touching the backend.
	c := &Config{Environments: map[string]EnvMap{
		"mac":   {"OP_TOKEN": Secret(keychainRef)},
		"linux": {"OP_TOKEN": Literal("local")},
	}}
	f := &fakeSecrets{reason: "not supported on linux"}
	x := secretExpander(f)
	cases := map[string]func() (map[string]string, error){
		"later named environment": func() (map[string]string, error) { return c.ResolveNamed([]string{"mac", "linux"}, x) },
		"literal env": func() (map[string]string, error) {
			return c.ResolveEnv([]string{"mac"}, EnvMap{"OP_TOKEN": Literal("local")}, nil, x)
		},
		"spawn env": func() (map[string]string, error) {
			return c.ResolveEnv([]string{"mac"}, nil, map[string]string{"OP_TOKEN": "local"}, x)
		},
		"inherited dispatch env": func() (map[string]string, error) {
			return c.DispatchEnv(TemplateConfig{Environments: []string{"mac"}}, []string{"linux"}, x)
		},
		"template literal under inherited": func() (map[string]string, error) {
			return c.DispatchEnv(TemplateConfig{Env: EnvMap{"OP_TOKEN": Secret(keychainRef)}}, []string{"linux"}, x)
		},
	}
	for name, run := range cases {
		got, err := run()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got["OP_TOKEN"] != "local" {
			t.Errorf("%s: OP_TOKEN = %q", name, got["OP_TOKEN"])
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("resolver called %d times for shadowed refs", len(f.calls))
	}
}

func TestEffectiveRefOverridesLowerLiteral(t *testing.T) {
	c := &Config{Environments: map[string]EnvMap{"base": {"K": Literal("lit")}}}
	f := &fakeSecrets{value: "sec"}
	got, err := c.ResolveEnv([]string{"base"}, EnvMap{"K": Secret(keychainRef)}, nil, secretExpander(f))
	if err != nil || got["K"] != "sec" {
		t.Errorf("K = %q, %v; want sec", got["K"], err)
	}
}

func TestTaskEnvResolvesRefs(t *testing.T) {
	c := &Config{}
	f := &fakeSecrets{value: "v"}
	got, err := c.TaskEnv(TaskConfig{Env: EnvMap{"K": Secret(keychainRef)}}, secretExpander(f))
	if err != nil || got["K"] != "v" {
		t.Errorf("TaskEnv = %v, %v", got, err)
	}
}

func TestMergeLayersTracksEffectiveSecrets(t *testing.T) {
	named := EnvLayer{Values: map[string]string{"A": "1"}, Secrets: map[string]ScopedRef{"B": {Scope: "environments.x", Key: "B", Ref: keychainRef}, "C": {Scope: "environments.x", Key: "C", Ref: keychainRef}}}
	top := ValuesLayer(map[string]string{"C": "over"})
	got := MergeLayers(named, top)
	if !reflect.DeepEqual(got.Values, map[string]string{"A": "1", "C": "over"}) {
		t.Errorf("values = %v", got.Values)
	}
	if len(got.Secrets) != 1 || got.Secrets["B"].Scope != "environments.x" {
		t.Errorf("secrets = %v", got.Secrets)
	}
}

func TestResolveTaskTargetCarriesSecretRefs(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"digest": {Runtime: "persistent", Env: EnvMap{"K": Secret(keychainRef)}}}}
	_, tmpl, _, err := cfg.ResolveTaskTarget("digest")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := tmpl.Env["K"].Ref(); !ok || r.Service != "OP" {
		t.Errorf("env K = %v, want the reference", tmpl.Env["K"])
	}
}

func TestShadowedFileRefPathIsNeverExpanded(t *testing.T) {
	// A lower layer's file ref whose path names an unset variable,
	// overridden by a literal: expanding it would fail the launch.
	c := &Config{Environments: map[string]EnvMap{
		"shared": {"OP_TOKEN": Secret(secretenv.Ref{Backend: secretenv.File, Path: "${UNSET_SECRET_DIR}/token"})},
		"local":  {"OP_TOKEN": Literal("local")},
	}}
	f := &fakeSecrets{value: "v"}
	x := secretExpander(f)
	got, err := c.ResolveNamed([]string{"shared", "local"}, x)
	if err != nil || got["OP_TOKEN"] != "local" {
		t.Fatalf("ResolveNamed = %v, %v", got, err)
	}
	got, err = c.ResolveEnv([]string{"shared"}, EnvMap{"OP_TOKEN": Literal("lit")}, nil, x)
	if err != nil || got["OP_TOKEN"] != "lit" {
		t.Fatalf("ResolveEnv = %v, %v", got, err)
	}
	if len(f.calls) != 0 {
		t.Errorf("resolver called for a shadowed ref: %v", f.calls)
	}
	// The effective ref still fails, naming the variable.
	_, err = c.ResolveNamed([]string{"shared"}, x)
	var unset *UnsetVariableError
	if !errors.As(err, &unset) || unset.Scope != "environments.shared" || unset.Key != "OP_TOKEN" {
		t.Errorf("effective unset path: %v", err)
	}
}
