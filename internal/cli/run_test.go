package cli

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// TestShouldRedactEnvKey verifies case-insensitive substring matching against
// the sensitive-key tokens.
func TestShouldRedactEnvKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"API_KEY", true},
		{"api_key", true},
		{"APIKey", true},
		{"MY_SECRET", true},
		{"secret", true},
		{"GITHUB_TOKEN", true},
		{"DB_PASSWORD", true},
		{"AWS_ACCESS_KEY_ID", true}, // contains KEY
		{"FOO_ACCESS_KEY", true},    // contains KEY
		{"PATH", false},
		{"LEO_CHANNELS", false},
		{"HOME", false},
		{"DEBUG", false},
		{"", false},
		{"APIKeyName", true}, // contains KEY
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			if got := shouldRedactEnvKey(tc.key); got != tc.want {
				t.Errorf("shouldRedactEnvKey(%q) = %v; want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestRedactValue verifies redaction output.
func TestRedactValue(t *testing.T) {
	cases := []struct {
		key, val, want string
	}{
		{"API_KEY", "sk-abc123", "<redacted>"},
		{"LEO_CHANNELS", "plugin:foo@bar", "plugin:foo@bar"},
		{"PASSWORD", "hunter2", "<redacted>"},
		{"token", "abc", "<redacted>"},
		{"DEBUG", "true", "true"},
	}
	for _, tc := range cases {
		if got := redactValue(tc.key, tc.val); got != tc.want {
			t.Errorf("redactValue(%q, %q) = %q; want %q", tc.key, tc.val, got, tc.want)
		}
	}
}

// TestTaskDryRunEnv verifies the env pairs returned for a task, sorted by key.
// Channels-only tasks populate LEO_CHANNELS; dev_channels populate LEO_DEV_CHANNELS.
func TestTaskDryRunEnv(t *testing.T) {
	cases := []struct {
		name string
		task config.TaskConfig
		want []envPair
	}{
		{
			name: "no channels",
			task: config.TaskConfig{},
			want: nil,
		},
		{
			name: "channels only",
			task: config.TaskConfig{Channels: []string{"plugin:telegram@x", "plugin:slack@y"}},
			want: []envPair{
				{key: "LEO_CHANNELS", display: "plugin:telegram@x,plugin:slack@y"},
			},
		},
		{
			name: "channels and dev channels sorted",
			task: config.TaskConfig{
				Channels:    []string{"plugin:telegram@x"},
				DevChannels: []string{"plugin:beta@z"},
			},
			want: []envPair{
				// LEO_CHANNELS sorts before LEO_DEV_CHANNELS alphabetically.
				{key: "LEO_CHANNELS", display: "plugin:telegram@x"},
				{key: "LEO_DEV_CHANNELS", display: "plugin:beta@z"},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := taskDryRunEnv(tc.task)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %+v want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
