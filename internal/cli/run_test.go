package cli

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/redact"
)

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
			name: "task env included, secrets masked",
			task: config.TaskConfig{
				Env: map[string]string{
					"OP_SERVICE_ACCOUNT_TOKEN": "ops_totally_fake_token_do_not_use",
					"ANTHROPIC_BASE_URL":       "http://localhost:3325",
				},
			},
			want: []envPair{
				{key: "ANTHROPIC_BASE_URL", display: "http://localhost:3325"},
				{key: "OP_SERVICE_ACCOUNT_TOKEN", display: redact.Mask},
			},
		},
		{
			// run.Run merges leo's vars over task.Env, so the dry run must
			// show one LEO_CHANNELS entry carrying the winning value.
			name: "task env colliding with LEO_CHANNELS yields one entry",
			task: config.TaskConfig{
				Channels: []string{"plugin:telegram@x"},
				Env:      map[string]string{"LEO_CHANNELS": "shadowed"},
			},
			want: []envPair{
				{key: "LEO_CHANNELS", display: "plugin:telegram@x"},
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
