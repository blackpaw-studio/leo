package service

import (
	"path/filepath"
)

// ServiceConfig holds everything needed to manage the leo service.
type ServiceConfig struct {
	LeoPath    string
	ConfigPath string
	WorkDir    string
	LogPath    string
	Env        map[string]string
}

// PidPath returns the path to the PID file for simple background mode.
func PidPath(workDir string) string {
	return filepath.Join(workDir, "state", "service.pid")
}

// LogPathFor returns the default log path for the service.
func LogPathFor(workDir string) string {
	return filepath.Join(workDir, "state", "service.log")
}
