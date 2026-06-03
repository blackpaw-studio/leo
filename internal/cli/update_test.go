package cli

import (
	"bytes"
	"strings"
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

func TestUpdateCmd_RejectsPRAndVersionTogether(t *testing.T) {
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--pr", "42", "--version", "pr-99-a1b2c3d"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --pr and --version are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should explain conflict; got %q", err)
	}
}

func TestUpdateCmd_RejectsNonPrereleaseVersion(t *testing.T) {
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--version", "v0.5.0"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --version is a stable tag")
	}
	if !strings.Contains(err.Error(), "prerelease tags only") {
		t.Errorf("error should explain --version limitation; got %q", err)
	}
}

func TestUpdateCmd_UnstableAndPRMutuallyExclusive(t *testing.T) {
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--unstable", "--pr", "5"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected mutual-exclusion error when --unstable and --pr are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should explain conflict; got %q", err)
	}
}

func TestUpdateCmd_UnstableAndVersionMutuallyExclusive(t *testing.T) {
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--unstable", "--version", "main-a1b2c3d"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected mutual-exclusion error when --unstable and --version are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should explain conflict; got %q", err)
	}
}
