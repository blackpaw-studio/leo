package service

import (
	"path/filepath"
)

// ServiceConfig holds everything needed to manage a chat service.
type ServiceConfig struct {
	AgentName  string
	LeoPath    string
	ConfigPath string
	WorkDir    string
	LogPath    string
	Env        map[string]string
}

// PidPath returns the path to the PID file for simple background mode.
func PidPath(workDir, agentName string) string {
	return filepath.Join(workDir, "state", "chat.pid")
}

// LogPathFor returns the default log path for the chat service.
func LogPathFor(workDir string) string {
	return filepath.Join(workDir, "state", "chat.log")
}
