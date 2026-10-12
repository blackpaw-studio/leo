package config

import (
	"fmt"
	"regexp"
	"time"

	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// ChatConfig configures Leo Chat (docs/configuration/chat.md).
type ChatConfig struct {
	// WhisperModel is the whisper.cpp ggml model voice notes are transcribed
	// with (large-v3-turbo recommended). Empty disables transcription: voice
	// notes are delivered with a "transcription failed" note.
	WhisperModel string `yaml:"whisper_model,omitempty"`
	// PermissionTimeout is how long a Claude permission prompt waits for an
	// answer from chat before the agent's own terminal prompt is left to
	// answer it (a Go duration; default 120s, at most 1h).
	PermissionTimeout string `yaml:"permission_timeout,omitempty"`
}

const (
	defaultChatPermissionTimeout = 120 * time.Second
	// MaxChatPermissionTimeout bounds chat.permission_timeout; the
	// PermissionRequest hook's own timeout is sized from it.
	MaxChatPermissionTimeout = claudeharness.AgentPermissionWait
)

// ChatPermissionTimeout is the parsed chat.permission_timeout; Validate
// rejects bad values, so a bad one here falls back to the default.
func (c *Config) ChatPermissionTimeout() time.Duration {
	if d, err := parseChatPermissionTimeout(c.Chat.PermissionTimeout); err == nil {
		return d
	}
	return defaultChatPermissionTimeout
}

func parseChatPermissionTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultChatPermissionTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d <= 0 || d > MaxChatPermissionTimeout {
		return 0, fmt.Errorf("must be positive and at most %s", MaxChatPermissionTimeout)
	}
	return d, nil
}

// ChatWhisperModel is chat.whisper_model with ~ expanded; "" when unset.
func (c *Config) ChatWhisperModel() string {
	if c.Chat.WhisperModel == "" {
		return ""
	}
	return expandHome(c.Chat.WhisperModel)
}

// agentNameRe is the shape of an agent name: lowercase alphanumerics and
// dashes, starting alphanumeric, at most 64 characters.
var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// IsAgentName reports whether name is a well-formed agent name. It says
// nothing about whether such an agent exists: agents are runtime state.
func IsAgentName(name string) bool { return agentNameRe.MatchString(name) }

func (c *Config) validateChat() []string {
	var errs []string
	if _, err := parseChatPermissionTimeout(c.Chat.PermissionTimeout); err != nil {
		errs = append(errs, fmt.Sprintf("chat.permission_timeout %q: %v", c.Chat.PermissionTimeout, err))
	}
	for name, task := range c.Tasks {
		if task.Owner != "" && !IsAgentName(task.Owner) {
			errs = append(errs, fmt.Sprintf("tasks.%s.owner %q is not a valid agent name (lowercase letters, digits and dashes)", name, task.Owner))
		}
	}
	return errs
}
