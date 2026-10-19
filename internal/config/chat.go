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
	// QuestionTimeout is how long an AskUserQuestion questions card waits for
	// an answer from chat before the agent's own terminal dialog is left to
	// answer it (a Go duration; default and maximum 1h, the hook's ceiling).
	// It is longer than the permission wait because questions are often
	// answered from a phone.
	QuestionTimeout string `yaml:"question_timeout,omitempty"`
}

const (
	defaultChatPermissionTimeout = 120 * time.Second
	// MaxChatPermissionTimeout bounds chat.permission_timeout; the
	// PermissionRequest hook's own timeout is sized from it.
	MaxChatPermissionTimeout = claudeharness.AgentPermissionWait
	// defaultChatQuestionTimeout is the hook ceiling: nobody's phone answer
	// should lose to a clock the hook would have outlived anyway.
	defaultChatQuestionTimeout = MaxChatPermissionTimeout
)

// ChatPermissionTimeout is the parsed chat.permission_timeout; Validate
// rejects bad values, so a bad one here falls back to the default.
func (c *Config) ChatPermissionTimeout() time.Duration {
	if d, err := parseChatPermissionTimeout(c.Chat.PermissionTimeout); err == nil {
		return d
	}
	return defaultChatPermissionTimeout
}

// ChatQuestionTimeout is the parsed chat.question_timeout; Validate rejects
// bad values, so a bad one here falls back to the default. It never exceeds
// MaxChatPermissionTimeout, so the hook always gets its answer before its own
// timeout.
func (c *Config) ChatQuestionTimeout() time.Duration {
	if d, err := parseChatTimeout(c.Chat.QuestionTimeout, defaultChatQuestionTimeout); err == nil {
		return d
	}
	return defaultChatQuestionTimeout
}

func parseChatPermissionTimeout(raw string) (time.Duration, error) {
	return parseChatTimeout(raw, defaultChatPermissionTimeout)
}

// parseChatTimeout parses a chat wait: empty is fallback, otherwise a
// positive duration of at most MaxChatPermissionTimeout.
func parseChatTimeout(raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
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
	if _, err := parseChatTimeout(c.Chat.QuestionTimeout, defaultChatQuestionTimeout); err != nil {
		errs = append(errs, fmt.Sprintf("chat.question_timeout %q: %v", c.Chat.QuestionTimeout, err))
	}
	for name, task := range c.Tasks {
		if task.Owner != "" && !IsAgentName(task.Owner) {
			errs = append(errs, fmt.Sprintf("tasks.%s.owner %q is not a valid agent name (lowercase letters, digits and dashes)", name, task.Owner))
		}
	}
	return errs
}
