package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fakeExpander(vars map[string]string) Expander {
	return Expander{
		Lookup: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		Home:   func() (string, error) { return "/home/evan", nil },
	}
}

func TestExpandValue(t *testing.T) {
	x := fakeExpander(map[string]string{"HOME": "/home/evan", "EMPTY": "", "A_1": "a"})
	tests := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"", ""},
		{"$HOME/.claude-b", "/home/evan/.claude-b"},
		{"${HOME}/x", "/home/evan/x"},
		{"${HOME}x", "/home/evanx"},
		{"$A_1-$A_1", "a-a"},
		{"$EMPTY/x", "/x"},
		{"~", "/home/evan"},
		{"~/.codex-b", "/home/evan/.codex-b"},
		{"~bob/x", "~bob/x"},
		{"a~/x", "a~/x"},
		{"$$HOME", "$HOME"},
		{"$${HOME}", "${HOME}"},
		{"cost$", "cost$"},
		{"a $ b", "a $ b"},
		{"$1", "$1"},
		{"~/$A_1", "/home/evan/a"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := x.Expand(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Expand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExpandValueUnsetVariable(t *testing.T) {
	_, err := fakeExpander(nil).Expand("$HOME/x ${NOPE}")
	var unset *UnsetVariableError
	if !errors.As(err, &unset) {
		t.Fatalf("want *UnsetVariableError, got %v", err)
	}
	if unset.Var != "HOME" {
		t.Fatalf("first unset var = %q, want HOME", unset.Var)
	}
}

func TestExpandValueMalformed(t *testing.T) {
	for _, in := range []string{"${HOME", "${}", "${a b}", "${1X}"} {
		if _, err := fakeExpander(map[string]string{"HOME": "/h"}).Expand(in); err == nil {
			t.Errorf("Expand(%q): want error", in)
		}
	}
}

func TestExpandValueHomeFailure(t *testing.T) {
	x := Expander{Lookup: func(string) (string, bool) { return "", false }, Home: func() (string, error) { return "", errors.New("no home") }}
	if _, err := x.Expand("~/x"); err == nil || !strings.Contains(err.Error(), "no home") {
		t.Fatalf("want home error, got %v", err)
	}
	if got, err := x.Expand("/abs"); err != nil || got != "/abs" {
		t.Fatalf("home must only be consulted for ~: %q, %v", got, err)
	}
}

func TestZeroExpanderUsesProcessEnvironment(t *testing.T) {
	t.Setenv("LEO_EXPAND_TEST", "/from/process")
	got, err := Expander{}.Expand("$LEO_EXPAND_TEST/x")
	if err != nil || got != "/from/process/x" {
		t.Fatalf("got %q, %v", got, err)
	}
	home, _ := os.UserHomeDir()
	if got, _ := (Expander{}).Expand("~/x"); got != filepath.Join(home, "x") {
		t.Fatalf("tilde = %q", got)
	}
}

func TestResolveEnvExpandsNamedAndLiteralNotSpawn(t *testing.T) {
	c := &Config{Environments: map[string]EnvMap{
		"b": LiteralEnv(map[string]string{"CLAUDE_CONFIG_DIR": "${HOME}/.claude-b", "PLAIN": "x"}),
	}}
	x := fakeExpander(map[string]string{"HOME": "/home/evan", "TOK": "secret"})
	got, err := c.ResolveEnv([]string{"b"}, LiteralEnv(map[string]string{"TOKEN": "$TOK", "ESC": "$$TOK"}), map[string]string{"SPAWN": "$TOK"}, x)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"CLAUDE_CONFIG_DIR": "/home/evan/.claude-b", "PLAIN": "x",
		"TOKEN": "secret", "ESC": "$TOK", "SPAWN": "$TOK",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if c.Environments["b"]["CLAUDE_CONFIG_DIR"] != Literal("${HOME}/.claude-b") {
		t.Fatal("config was mutated by expansion")
	}
}

func TestResolveEnvUnsetVariableNamesScopeKeyAndVariable(t *testing.T) {
	c := &Config{Environments: map[string]EnvMap{"b": LiteralEnv(map[string]string{"CLAUDE_CONFIG_DIR": "$NOPE/x"})}}
	x := fakeExpander(nil)

	_, err := c.ResolveEnv([]string{"b"}, nil, nil, x)
	var unset *UnsetVariableError
	if !errors.As(err, &unset) {
		t.Fatalf("want *UnsetVariableError, got %v", err)
	}
	for _, part := range []string{"environments.b", "CLAUDE_CONFIG_DIR", "$NOPE"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q missing %q", err, part)
		}
	}

	_, err = c.ResolveEnv(nil, LiteralEnv(map[string]string{"TOKEN": "${MISSING}"}), nil, x)
	if err == nil || !strings.Contains(err.Error(), "env.TOKEN") || !strings.Contains(err.Error(), "$MISSING") {
		t.Fatalf("literal env error = %v", err)
	}
}

func TestResolveEnvOverriddenLayerStillMustExpand(t *testing.T) {
	// A key shadowed by a higher layer is still validated: a typo in a lower
	// layer should not hide until someone removes the override.
	c := &Config{Environments: map[string]EnvMap{"b": LiteralEnv(map[string]string{"K": "$NOPE"})}}
	if _, err := c.ResolveEnv([]string{"b"}, LiteralEnv(map[string]string{"K": "x"}), nil, fakeExpander(nil)); err == nil {
		t.Fatal("want error for unset variable in shadowed layer")
	}
}

func TestTaskAndTemplateEnvExpand(t *testing.T) {
	c := &Config{}
	x := fakeExpander(map[string]string{"HOME": "/h"})
	te, err := c.TaskEnv(TaskConfig{Env: LiteralEnv(map[string]string{"D": "$HOME/t"})}, x)
	if err != nil || te["D"] != "/h/t" {
		t.Fatalf("TaskEnv = %v, %v", te, err)
	}
	me, err := c.TemplateEnv(TemplateConfig{Env: LiteralEnv(map[string]string{"D": "~/m"})}, x)
	if err != nil || me["D"] != "/home/evan/m" {
		t.Fatalf("TemplateEnv = %v, %v", me, err)
	}
	if _, err := c.TaskEnv(TaskConfig{Env: LiteralEnv(map[string]string{"D": "$NOPE"})}, x); err == nil {
		t.Fatal("TaskEnv: want unset error")
	}
}

func TestResolveNamedExpandsAndRejectsUnknown(t *testing.T) {
	c := &Config{Environments: map[string]EnvMap{"b": LiteralEnv(map[string]string{"K": "$HOME/x"})}}
	x := fakeExpander(map[string]string{"HOME": "/h"})
	got, err := c.ResolveNamed([]string{"b"}, x)
	if err != nil || got["K"] != "/h/x" {
		t.Fatalf("got %v, %v", got, err)
	}
	var unknown *UnknownEnvironmentError
	if _, err := c.ResolveNamed([]string{"zz"}, x); !errors.As(err, &unknown) {
		t.Fatalf("want unknown-environment error, got %v", err)
	}
}

func TestEnvironmentWarningsFlagVariablesUnsetInProcess(t *testing.T) {
	c := minimalValidConfig()
	c.Environments = map[string]EnvMap{
		"acct-b": LiteralEnv(map[string]string{"CLAUDE_CONFIG_DIR": "$ACCT_ROOT/.claude", "FINE": "${HOME}/x", "TWICE": "$ACCT_ROOT/$ACCT_ROOT"}),
	}
	c.Templates = map[string]TemplateConfig{"t": {Env: LiteralEnv(map[string]string{"T": "$TMPL_VAR"})}}
	c.Tasks["job"] = TaskConfig{Env: LiteralEnv(map[string]string{"J": "$TASK_VAR"})}
	x := fakeExpander(map[string]string{"HOME": "/h"})

	got := c.EnvironmentWarnings(x)
	joined := strings.Join(got, "\n")
	for _, part := range []string{"environments.acct-b.CLAUDE_CONFIG_DIR", "$ACCT_ROOT", "templates.t.env.T", "$TMPL_VAR", "tasks.job.env.J", "$TASK_VAR"} {
		if !strings.Contains(joined, part) {
			t.Errorf("warnings missing %q:\n%s", part, joined)
		}
	}
	if strings.Contains(joined, "FINE") {
		t.Errorf("set variable flagged:\n%s", joined)
	}
	if len(got) != 4 {
		t.Errorf("want 4 warnings (one per key), got %d:\n%s", len(got), joined)
	}
}

func TestLoadSaveKeepsVariableReferencesLiteral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	src := "defaults:\n  model: sonnet\ntemplates:\n  t:\n    env:\n      D: ${HOME}/d\nenvironments:\n  acct-b:\n    CLAUDE_CONFIG_DIR: ${HOME}/.claude-b\n    CODEX_HOME: ~/.codex-b\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Environments["acct-b"]["CLAUDE_CONFIG_DIR"]; got != Literal("${HOME}/.claude-b") {
		t.Fatalf("Load expanded value: %q", got)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	for _, want := range []string{"${HOME}/.claude-b", "${HOME}/d", "~/.codex-b"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("saved yaml lost %q:\n%s", want, out)
		}
	}
}

func TestExpansionErrorsNeverEchoValueContents(t *testing.T) {
	const secret = "hunter2-secret"
	x := fakeExpander(nil)
	cases := map[string]string{
		"unterminated": "prefix${" + secret + "-tail",
		"bad name":     "pw-${" + secret + " x}",
		"unset":        "pw-" + secret + "-$NOPE",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			c := &Config{Environments: map[string]EnvMap{"b": LiteralEnv(map[string]string{"TOKEN": value})}, Templates: map[string]TemplateConfig{"t": {Env: LiteralEnv(map[string]string{"T": value})}}}
			_, err := c.ResolveEnv([]string{"b"}, nil, nil, x)
			if err == nil {
				t.Fatal("want error")
			}
			msgs := append([]string{err.Error()}, c.EnvironmentWarnings(x)...)
			for _, m := range msgs {
				if strings.Contains(m, secret) {
					t.Errorf("message leaks value contents: %q", m)
				}
			}
			if name != "unset" && !strings.Contains(err.Error(), "environments.b.TOKEN") {
				t.Errorf("error lacks scope/key: %q", err)
			}
		})
	}
	_, err := x.Expand("ab${oops")
	var bad *MalformedReferenceError
	if !errors.As(err, &bad) || bad.Offset != 2 {
		t.Fatalf("want *MalformedReferenceError at byte 2, got %v", err)
	}
	_, err = fakeExpander(nil).Expand("$NOPE")
	if err == nil || !strings.Contains(err.Error(), "$NOPE") {
		t.Fatalf("unset error must name the variable: %v", err)
	}
}
