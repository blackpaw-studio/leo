package service

import (
	"fmt"
	"os"
	"path/filepath"
)

const maxRotatedLogs = 3

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

// RotateLog rotates the service log file, keeping the last N rotated copies.
// Existing service.log -> service.log.1, service.log.1 -> service.log.2, etc.
func RotateLog(logPath string) error {
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return nil // nothing to rotate
	}

	// Remove the oldest rotated log to make room
	os.Remove(fmt.Sprintf("%s.%d", logPath, maxRotatedLogs))

	// Shift existing rotated logs
	for i := maxRotatedLogs - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", logPath, i)
		dst := fmt.Sprintf("%s.%d", logPath, i+1)
		os.Rename(src, dst) // ignore errors — file may not exist
	}

	// Rotate current log
	return os.Rename(logPath, logPath+".1")
}
