package opencode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/tmuxtui"
)

// TestOpencodeDriverWiring locks the capabilities the shared tmuxtui driver
// exposes for opencode: quick-exit recovery and session-args refresh
// (opencode has no PreLaunch hook, unlike codex).
func TestOpencodeDriverWiring(t *testing.T) {
	d := (Opencode{}).Driver()
	if _, ok := d.(harness.SessionArgsRefresher); !ok {
		t.Fatalf("Driver() does not implement harness.SessionArgsRefresher")
	}
	if _, ok := d.(harness.QuickExitRecovery); !ok {
		t.Fatalf("Driver() does not implement harness.QuickExitRecovery")
	}
	if _, ok := d.(tmuxtui.Driver); !ok {
		t.Fatalf("Driver() is not a tmuxtui.Driver")
	}
}

func TestValidateModel(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		wantErr string
	}{
		{name: "empty ok", model: ""},
		{name: "provider/model ok", model: "anthropic/claude-sonnet-4-5"},
		{name: "no slash errors", model: "opus", wantErr: `"opus" is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)`},
		{name: "empty model errors", model: "a/", wantErr: `"a/" is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)`},
		{name: "empty provider errors", model: "/b", wantErr: `"/b" is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Opencode{}.ValidateModel(tt.model)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("got error %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSupportsChannels(t *testing.T) {
	if (Opencode{}).SupportsChannels() {
		t.Error("expected SupportsChannels to be false")
	}
}

func TestSupportsKind(t *testing.T) {
	tests := []struct {
		kind harness.Kind
		want bool
	}{
		{harness.KindTask, true},
		{harness.KindAgent, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := (Opencode{}).SupportsKind(tt.kind); got != tt.want {
				t.Errorf("SupportsKind(%s) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestSessionArgs(t *testing.T) {
	tests := []struct {
		name  string
		state harness.SessionState
		want  []string
	}{
		{
			name:  "resume",
			state: harness.SessionState{Mode: harness.SessionResume, ID: "ses_1"},
			want:  []string{"-s", "ses_1"},
		},
		{
			name:  "none",
			state: harness.SessionState{Mode: harness.SessionNone},
			want:  nil,
		},
		{
			name:  "pinned",
			state: harness.SessionState{Mode: harness.SessionPinned, ID: "ses_2"},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Opencode{}.SessionArgs(tt.state)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		want []string
	}{
		{
			name: "fresh minimal",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "do it", Options: Options{}},
			want: []string{"run", "--format", "json", "do it"},
		},
		{
			name: "model and resume",
			spec: harness.LaunchSpec{
				Kind: harness.KindTask, Prompt: "again", Model: "anthropic/claude-sonnet-4-5",
				Session: harness.SessionState{Mode: harness.SessionResume, ID: "ses_42"},
				Options: Options{},
			},
			want: []string{"run", "--format", "json",
				"--model", "anthropic/claude-sonnet-4-5", "-s", "ses_42", "again"},
		},
		{
			name: "KindAgent TUI argv with model",
			spec: harness.LaunchSpec{
				Kind: harness.KindAgent, Model: "lmstudio/qwen/qwen3.6-35b-a3b",
				Options: Options{},
			},
			want: []string{"--model", "lmstudio/qwen/qwen3.6-35b-a3b"},
		},
		{
			name: "KindAgent TUI argv without model",
			spec: harness.LaunchSpec{
				Kind:    harness.KindAgent,
				Options: Options{},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Opencode{}.Args(tt.spec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestArgsErrors(t *testing.T) {
	tests := []struct {
		name    string
		spec    harness.LaunchSpec
		wantErr string
	}{
		{
			name:    "SessionPinned",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}, Session: harness.SessionState{Mode: harness.SessionPinned, ID: "x"}},
			wantErr: `opencode: cannot start a session with a pre-issued ID`,
		},
		{
			name:    "wrong Options type",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: "not-opencode-options"},
			wantErr: `opencode: spec.Options is string, want opencode.Options`,
		},
		{
			name:    "channels unsupported",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}, Channels: []string{"plugin:telegram"}},
			wantErr: `opencode: channel plugins are not supported; use leo's MCP tools for messaging`,
		},
		{
			name:    "dev channels unsupported",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}, DevChannels: []string{"plugin:telegram"}},
			wantErr: `opencode: channel plugins are not supported; use leo's MCP tools for messaging`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Opencode{}.Args(tt.spec)
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("got error %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestEnvBuildsConfigContent(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindTask, Options: Options{
		Permission: map[string]any{"bash": "deny"},
		LeoMCP: &LeoMCPBridge{
			Command:     []string{"leo", "mcp-server"},
			Env:         map[string]string{"LEO_PROCESS_NAME": "task:t", "LEO_WEB_PORT": "8080", "LEO_API_TOKEN": "tok"},
			ToolTimeout: 32 * time.Minute,
		},
	}}
	env, err := Opencode{}.Env(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw := env["OPENCODE_CONFIG_CONTENT"]
	var cfg struct {
		MCP map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Enabled     bool              `json:"enabled"`
			Environment map[string]string `json:"environment"`
			Timeout     int64             `json:"timeout"`
		} `json:"mcp"`
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config content is not JSON: %v\n%s", err, raw)
	}
	leo := cfg.MCP["leo"]
	if leo.Type != "local" || !leo.Enabled || !reflect.DeepEqual(leo.Command, []string{"leo", "mcp-server"}) {
		t.Errorf("mcp.leo = %+v", leo)
	}
	// opencode's per-server timeout is milliseconds.
	if leo.Timeout != (32 * time.Minute).Milliseconds() {
		t.Errorf("mcp.leo.timeout = %d, want %d", leo.Timeout, (32 * time.Minute).Milliseconds())
	}
	if leo.Environment["LEO_API_TOKEN"] != "tok" {
		t.Errorf("environment = %+v", leo.Environment)
	}
	if cfg.Permission["bash"] != "deny" {
		t.Errorf("permission = %+v", cfg.Permission)
	}
}

func TestEnvPermissionOnly(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindTask, Options: Options{
		Permission: map[string]any{"bash": "deny"},
	}}
	env, err := Opencode{}.Env(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw := env["OPENCODE_CONFIG_CONTENT"]
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config content is not JSON: %v\n%s", err, raw)
	}
	if _, ok := cfg["permission"]; !ok {
		t.Errorf("expected permission key, got %v", cfg)
	}
	if _, ok := cfg["mcp"]; ok {
		t.Errorf("expected no mcp key, got %v", cfg)
	}
}

func TestEnvLeoMCPOnly(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindTask, Options: Options{
		LeoMCP: &LeoMCPBridge{Command: []string{"leo", "mcp-server"}},
	}}
	env, err := Opencode{}.Env(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw := env["OPENCODE_CONFIG_CONTENT"]
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config content is not JSON: %v\n%s", err, raw)
	}
	if _, ok := cfg["mcp"]; !ok {
		t.Errorf("expected mcp key, got %v", cfg)
	}
	// A zero ToolTimeout leaves opencode's own default in place.
	if strings.Contains(raw, "timeout") {
		t.Errorf("expected no timeout key without a ToolTimeout, got %s", raw)
	}
	if _, ok := cfg["permission"]; ok {
		t.Errorf("expected no permission key, got %v", cfg)
	}
}

func TestEnvNeitherReturnsNil(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}}
	env, err := Opencode{}.Env(spec)
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Errorf("got %v, want nil", env)
	}
}

// TestEnvNeverEmitsServerPassword locks the deletion of the resident
// `opencode serve` machinery: no Options field can populate
// OPENCODE_SERVER_PASSWORD anymore, so Env must never emit it, even when
// every other overlay knob (permission + LeoMCP) is populated.
func TestEnvNeverEmitsServerPassword(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindTask, Options: Options{
		Permission: map[string]any{"bash": "deny"},
		LeoMCP:     &LeoMCPBridge{Command: []string{"leo", "mcp-server"}},
	}}
	env, err := Opencode{}.Env(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["OPENCODE_SERVER_PASSWORD"]; ok {
		t.Errorf("Env() emitted OPENCODE_SERVER_PASSWORD = %q, want it gone entirely", env["OPENCODE_SERVER_PASSWORD"])
	}
}
