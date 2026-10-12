package config

import "testing"

func TestPushEnabled(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name     string
		defaults PushConfig
		tmpl     *PushConfig // nil: template has no push block
		template string
		state    string
		want     bool
	}{
		{name: "nothing configured pushes", template: "t", state: "finished", want: true},
		{name: "template off wins over defaults on", defaults: PushConfig{Finished: &on}, tmpl: &PushConfig{Finished: &off}, template: "t", state: "finished", want: false},
		{name: "template on wins over defaults off", defaults: PushConfig{Errored: &off}, tmpl: &PushConfig{Errored: &on}, template: "t", state: "errored", want: true},
		{name: "template unset falls to defaults off", defaults: PushConfig{NeedsInput: &off}, tmpl: &PushConfig{Finished: &off}, template: "t", state: "needs_input", want: false},
		{name: "states cascade independently", defaults: PushConfig{Finished: &off}, tmpl: &PushConfig{NeedsInput: &off}, template: "t", state: "errored", want: true},
		{name: "unknown template uses defaults", defaults: PushConfig{Finished: &off}, template: "missing", state: "finished", want: false},
		{name: "empty template uses defaults", defaults: PushConfig{Finished: &off}, template: "", state: "finished", want: false},
		{name: "unknown state pushes", defaults: PushConfig{Finished: &off}, template: "t", state: "weird", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Defaults: DefaultsConfig{Push: tt.defaults}}
			if tt.tmpl != nil {
				cfg.Templates = map[string]TemplateConfig{"t": {Push: *tt.tmpl}}
			}
			if got := cfg.PushEnabled(tt.template, tt.state); got != tt.want {
				t.Fatalf("PushEnabled(%q, %q) = %v, want %v", tt.template, tt.state, got, tt.want)
			}
		})
	}
}
