package config

import (
	"errors"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

func svcRef(service string) secretenv.Ref {
	return secretenv.Ref{Backend: secretenv.Keychain, Service: service}
}

func TestSecretReferencesIsTheEffectiveSet(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{Environments: []string{"base"}},
		Environments: map[string]EnvMap{
			"base":  {"BASE": Secret(svcRef("BASE"))},
			"work":  {"OP": Secret(svcRef("OP")), "SHADOWED": Secret(svcRef("SHADOWED"))},
			"spare": {"S": Secret(svcRef("SPARE"))}, // used by nothing: only a spawn-time list applies it
		},
		Templates: map[string]TemplateConfig{
			"coder":  {Environments: []string{"work"}, Env: EnvMap{"SHADOWED": Literal("x"), "T": Secret(svcRef("T"))}},
			"helper": {Environments: []string{"work"}, Env: EnvMap{"SHADOWED": Literal("y")}},
			"plain":  {}, // inherits defaults.environments
		},
		Tasks: map[string]TaskConfig{
			"beat": {Environments: []string{}, Env: EnvMap{"X": Secret(svcRef("X")), "L": Literal("l")}},
		},
	}
	want := []ScopedRef{
		{Scope: "environments.base", Key: "BASE", Ref: svcRef("BASE")},
		{Scope: "environments.spare", Key: "S", Ref: svcRef("SPARE")},
		{Scope: "environments.work", Key: "OP", Ref: svcRef("OP")},
		{Scope: "tasks.beat.env", Key: "X", Ref: svcRef("X")},
		{Scope: "templates.coder.env", Key: "T", Ref: svcRef("T")},
	}
	if got := cfg.SecretReferences(); !reflect.DeepEqual(got, want) {
		t.Errorf("SecretReferences =\n%+v\nwant\n%+v", got, want)
	}
}

func TestCheckSecretExpandsAndClassifies(t *testing.T) {
	var got []secretenv.Ref
	x := fakeExpander(map[string]string{"HOME": "/home/evan"})
	x.Secrets = resolverFunc(func(r secretenv.Ref) (string, error) {
		got = append(got, r)
		switch r.Service {
		case "OK":
			return "v", nil
		case "AWAY":
			return "", &secretenv.ResolutionError{Ref: r, Reason: "not supported on linux", Unavailable: true}
		}
		return "", &secretenv.ResolutionError{Ref: r, Reason: "item not found"}
	})
	if err := x.CheckSecret(ScopedRef{Scope: "environments.e", Key: "F", Ref: secretenv.Ref{Backend: secretenv.File, Path: "~/tok"}}); err == nil {
		// resolverFunc fails anything not named OK; the path is what matters here.
		t.Fatal("want the fake's failure")
	}
	if got[0].Path != "/home/evan/tok" {
		t.Errorf("resolved path = %q, want it expanded", got[0].Path)
	}
	if err := x.CheckSecret(ScopedRef{Scope: "s", Key: "K", Ref: svcRef("OK")}); err != nil {
		t.Errorf("ok ref: %v", err)
	}
	var se *SecretResolutionError
	err := x.CheckSecret(ScopedRef{Scope: "environments.e", Key: "K", Ref: svcRef("AWAY")})
	if !errors.As(err, &se) || !se.Unavailable || se.Error() != "environments.e.K: secret keychain(service=AWAY): not supported on linux" {
		t.Errorf("unavailable: %#v", err)
	}
	err = x.CheckSecret(ScopedRef{Scope: "environments.e", Key: "K", Ref: svcRef("GONE")})
	if !errors.As(err, &se) || se.Unavailable {
		t.Errorf("failure: %#v", err)
	}
}
