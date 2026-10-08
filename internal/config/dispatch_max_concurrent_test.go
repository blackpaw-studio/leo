package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDispatchMaxConcurrent(t *testing.T) {
	intp := func(n int) *int { return &n }
	tests := []struct {
		name    string
		set     *int
		want    int
		invalid bool
	}{
		{name: "unset defaults to six", set: nil, want: 6},
		{name: "explicit cap", set: intp(2), want: 2},
		{name: "zero means unlimited", set: intp(0), want: 0},
		{name: "negative is rejected", set: intp(-1), invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Defaults.Dispatch.MaxConcurrent = tt.set
			err := cfg.Validate()
			if tt.invalid {
				if err == nil {
					t.Fatal("negative max_concurrent accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.DispatchMaxConcurrent(); got != tt.want {
				t.Fatalf("DispatchMaxConcurrent() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDispatchMaxConcurrentYAMLDistinguishesZeroFromUnset(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("defaults:\n  dispatch:\n    max_concurrent: 0\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.DispatchMaxConcurrent(); got != 0 {
		t.Fatalf("explicit 0 read as %d, want unlimited (0)", got)
	}
}
