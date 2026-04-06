package daemon

import "encoding/json"

// Response is the standard envelope for all daemon API responses.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// CronRequest is the body for POST /cron/install and /cron/remove.
type CronRequest struct {
	ConfigPath string `json:"config_path"`
}

// TaskAddRequest is the body for POST /task/add.
type TaskAddRequest struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	PromptFile string `json:"prompt_file"`
	Model      string `json:"model,omitempty"`
	MaxTurns   int    `json:"max_turns,omitempty"`
	Topic      string `json:"topic,omitempty"`
	Silent     bool   `json:"silent,omitempty"`
	Enabled    bool   `json:"enabled"`
}

// TaskNameRequest is the body for POST /task/remove, /task/enable, /task/disable.
type TaskNameRequest struct {
	Name string `json:"name"`
}
