package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunRootNoArgs(t *testing.T) {
	tests := []struct {
		name      string
		cfgPath   string
		findErr   error
		wantHint  string
		avoidHint string
	}{
		{
			name:      "explicit --config path implies a config",
			cfgPath:   "/tmp/leo.yaml",
			findErr:   errors.New("unused — cfgPath short-circuits"),
			wantHint:  "leo status",
			avoidHint: "leo setup",
		},
		{
			name:      "auto-detected config",
			cfgPath:   "",
			findErr:   nil,
			wantHint:  "leo status",
			avoidHint: "leo setup",
		},
		{
			name:      "no config found",
			cfgPath:   "",
			findErr:   errors.New("leo.yaml not found"),
			wantHint:  "leo setup",
			avoidHint: "leo status",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := findConfigFn
			t.Cleanup(func() { findConfigFn = orig })
			findConfigFn = func(string) (string, error) {
				if tc.findErr != nil {
					return "", tc.findErr
				}
				return "/tmp/leo.yaml", nil
			}

			var buf bytes.Buffer
			runRootNoArgs(&buf, tc.cfgPath)
			out := buf.String()

			if !strings.Contains(out, rootLong) {
				t.Errorf("output missing rootLong text:\n%s", out)
			}
			if !strings.Contains(out, "Getting started:") {
				t.Errorf("output missing 'Getting started:' header:\n%s", out)
			}
			if !strings.Contains(out, tc.wantHint) {
				t.Errorf("output missing %q:\n%s", tc.wantHint, out)
			}
			if strings.Contains(out, tc.avoidHint) {
				t.Errorf("output unexpectedly contains %q:\n%s", tc.avoidHint, out)
			}
		})
	}
}

func TestRootCmd_VersionFlag(t *testing.T) {
	origVersion := Version
	t.Cleanup(func() { Version = origVersion })
	Version = "test-1.2.3"

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"--version"}},
		{name: "short flag", args: []string{"-v"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootCmd()
			if cmd.Version != "test-1.2.3" {
				t.Errorf("rootCmd.Version = %q, want %q", cmd.Version, "test-1.2.3")
			}

			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute %v: %v", tc.args, err)
			}

			got := buf.String()
			want := "leo test-1.2.3\n"
			if got != want {
				t.Errorf("%v output = %q, want %q", tc.args, got, want)
			}
		})
	}
}
