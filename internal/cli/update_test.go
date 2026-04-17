package cli

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/update"
)

func TestAllowUnsignedFromEnv(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		// Unset / empty → strict default.
		{"", false},
		// Truthy values — signature fallback enabled.
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"t", true},
		{"T", true},
		{"yes", true},
		{"Yes", true},
		{"YES", true},
		{"on", true},
		{"On", true},
		{"ON", true},
		// Falsy values — strict mode.
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"False", false},
		{"f", false},
		{"F", false},
		{"no", false},
		{"No", false},
		{"NO", false},
		{"off", false},
		{"Off", false},
		{"OFF", false},
		// Unrecognised values → strict default. The previous
		// "any non-empty wins" implementation flipped this the wrong way.
		{"maybe", false},
		{"garbage", false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(update.UnsignedReleaseEnv, tt.value)
			if got := allowUnsignedFromEnv(); got != tt.want {
				t.Errorf("allowUnsignedFromEnv() with %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
