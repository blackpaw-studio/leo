package config

import (
	"strings"
	"testing"
	"time"
)

func TestChatPermissionTimeout(t *testing.T) {
	cfg := &Config{}
	if got := cfg.ChatPermissionTimeout(); got != 120*time.Second {
		t.Fatalf("default = %s, want 2m", got)
	}
	cfg.Chat.PermissionTimeout = "45s"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ChatPermissionTimeout(); got != 45*time.Second {
		t.Fatalf("configured = %s", got)
	}
	for _, bad := range []string{"soon", "0s", "-1s", "2h"} {
		cfg.Chat.PermissionTimeout = bad
		if cfg.Validate() == nil {
			t.Fatalf("permission_timeout %q accepted", bad)
		}
	}
}

func TestChatQuestionTimeout(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		invalid bool
	}{
		{name: "unset defaults to the hook ceiling", raw: "", want: MaxChatPermissionTimeout},
		{name: "configured", raw: "30m", want: 30 * time.Minute},
		{name: "the ceiling itself", raw: "1h", want: time.Hour},
		{name: "not a duration", raw: "soon", invalid: true},
		{name: "zero", raw: "0s", invalid: true},
		{name: "negative", raw: "-1s", invalid: true},
		{name: "above the hook ceiling", raw: "2h", invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Chat: ChatConfig{QuestionTimeout: tc.raw}}
			err := cfg.Validate()
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "chat.question_timeout") {
					t.Fatalf("question_timeout %q accepted (err %v)", tc.raw, err)
				}
				if got := cfg.ChatQuestionTimeout(); got > MaxChatPermissionTimeout {
					t.Fatalf("invalid value yields %s, past the hook ceiling", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.ChatQuestionTimeout(); got != tc.want {
				t.Fatalf("ChatQuestionTimeout = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestChatQuestionTimeoutIsIndependentOfPermissionTimeout(t *testing.T) {
	cfg := &Config{Chat: ChatConfig{PermissionTimeout: "45s"}}
	if got := cfg.ChatPermissionTimeout(); got != 45*time.Second {
		t.Fatalf("permission = %s", got)
	}
	if got := cfg.ChatQuestionTimeout(); got != MaxChatPermissionTimeout {
		t.Fatalf("question = %s, want the default %s", got, MaxChatPermissionTimeout)
	}
}

func TestChatWhisperModelExpandsHome(t *testing.T) {
	cfg := &Config{Chat: ChatConfig{WhisperModel: "~/models/ggml.bin"}}
	if got := cfg.ChatWhisperModel(); strings.HasPrefix(got, "~") || !strings.HasSuffix(got, "/models/ggml.bin") {
		t.Fatalf("whisper model = %q", got)
	}
	if got := (&Config{}).ChatWhisperModel(); got != "" {
		t.Fatalf("unset model = %q", got)
	}
}

func TestTaskOwnerIsValidatedAsAnAgentNameOnly(t *testing.T) {
	task := TaskConfig{Schedule: "0 7 * * *", PromptFile: "p.md"}
	for _, ok := range []string{"", "assistant", "web-2", "a"} {
		task.Owner = ok
		cfg := &Config{Tasks: map[string]TaskConfig{"daily": task}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("owner %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"Assistant", "leo assistant", "../x", "-lead", strings.Repeat("a", 65)} {
		task.Owner = bad
		cfg := &Config{Tasks: map[string]TaskConfig{"daily": task}}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "tasks.daily.owner") {
			t.Fatalf("owner %q accepted (err %v)", bad, err)
		}
	}
}
