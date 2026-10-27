package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func moveErrors(cfg *Config) string {
	err := cfg.Validate()
	if err == nil {
		return ""
	}
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.Contains(line, "move") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func TestMoveConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		global  MoveConfig
		tmpl    MoveConfig
		wantErr string // "" means no move error
	}{
		{name: "valid", global: MoveConfig{Exclude: []string{"node_modules/"}, HookTimeout: "5m",
			Hooks: MoveHooksConfig{PreSend: []string{"~/bin/export.sh"}, PostReceive: []string{"/usr/local/bin/import"}}}},
		{name: "unparseable timeout", global: MoveConfig{HookTimeout: "soon"}, wantErr: "move.hook_timeout"},
		{name: "non-positive timeout", tmpl: MoveConfig{HookTimeout: "0s"}, wantErr: "templates.t.move.hook_timeout"},
		{name: "relative hook", global: MoveConfig{Hooks: MoveHooksConfig{PreSend: []string{"bin/export.sh"}}}, wantErr: "move.hooks.pre_send"},
		{name: "empty hook", tmpl: MoveConfig{Hooks: MoveHooksConfig{PostReceive: []string{""}}}, wantErr: "templates.t.move.hooks.post_receive"},
		{name: "hook with args", global: MoveConfig{Hooks: MoveHooksConfig{PreSend: []string{"/bin/sh -c x"}}}, wantErr: "move.hooks.pre_send"},
		{name: "empty exclude", global: MoveConfig{Exclude: []string{""}}, wantErr: "move.exclude"},
		{name: "exclude with newline", tmpl: MoveConfig{Exclude: []string{"a\nb"}}, wantErr: "templates.t.move.exclude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{HomePath: t.TempDir(), Move: tt.global, Templates: map[string]TemplateConfig{"t": {Move: tt.tmpl}}}
			got := moveErrors(cfg)
			if tt.wantErr == "" {
				if got != "" {
					t.Fatalf("unexpected move errors: %s", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantErr) {
				t.Fatalf("move errors = %q, want one naming %q", got, tt.wantErr)
			}
		})
	}
}

func TestTemplateMoveMergesGlobal(t *testing.T) {
	home, _ := os.UserHomeDir()
	cfg := &Config{
		Move: MoveConfig{Exclude: []string{"node_modules/"}, HookTimeout: "2m",
			Hooks: MoveHooksConfig{PreSend: []string{"~/bin/g-pre"}, PostReceive: []string{"/g-post"}}},
		Templates: map[string]TemplateConfig{
			"t":    {Move: MoveConfig{Exclude: []string{".build/"}, HookTimeout: "30s", Hooks: MoveHooksConfig{PreSend: []string{"/t-pre"}}}},
			"bare": {},
		},
	}
	got := cfg.ResolveMove("t")
	if want := []string{"node_modules/", ".build/"}; !reflect.DeepEqual(got.Exclude, want) {
		t.Errorf("Exclude = %v, want %v", got.Exclude, want)
	}
	if want := []string{filepath.Join(home, "bin/g-pre"), "/t-pre"}; !reflect.DeepEqual(got.PreSend, want) {
		t.Errorf("PreSend = %v, want %v", got.PreSend, want)
	}
	if want := []string{"/g-post"}; !reflect.DeepEqual(got.PostReceive, want) {
		t.Errorf("PostReceive = %v, want %v", got.PostReceive, want)
	}
	if got.HookTimeout != 30*time.Second {
		t.Errorf("HookTimeout = %v, want the template's 30s", got.HookTimeout)
	}
	if bare := cfg.ResolveMove("bare"); bare.HookTimeout != 2*time.Minute || len(bare.PreSend) != 1 {
		t.Errorf("bare template = %+v, want the global block", bare)
	}
	if none := (&Config{}).ResolveMove("missing"); none.HookTimeout != DefaultMoveHookTimeout || none.Exclude != nil {
		t.Errorf("no move config = %+v, want the default timeout and nothing else", none)
	}
}

func TestTemplatesFileAcceptsMove(t *testing.T) {
	const shared = "templates:\n  t:\n    model: opus\n    move:\n      exclude: [node_modules/]\n      hooks:\n        pre_send: [/bin/export]\n"
	dir := t.TempDir()
	leoPath := writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\n")
	writeFile(t, dir, "shared.yaml", shared)
	cfg, err := Load(leoPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Templates["t"].Move; len(got.Hooks.PreSend) != 1 || got.Hooks.PreSend[0] != "/bin/export" || got.Exclude[0] != "node_modules/" {
		t.Fatalf("shared template move = %+v", got)
	}
	// An edit elsewhere in the entry keeps the move block as written.
	out := editSharedAndRead(t, shared, setModel("t", "sonnet"))
	if !strings.Contains(out, "model: sonnet\n    move:\n      exclude: [node_modules/]\n      hooks:\n        pre_send: [/bin/export]\n") {
		t.Fatalf("move block not kept:\n%s", out)
	}
}
