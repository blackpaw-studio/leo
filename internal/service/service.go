package service

import (
	"io"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Defaults for the rotating service log. Chosen to comfortably cover a
// solo-user install while bounding disk use even under a crash loop.
const (
	logMaxSizeMB  = 10
	logMaxBackups = 3
	logMaxAgeDays = 30
	logCompress   = true
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

// NewRotatingLogWriter returns a size-based rotating writer for the daemon
// log. Rotation is handled by lumberjack on every Write that crosses the
// size threshold, so it does not depend on daemon restart cadence.
func NewRotatingLogWriter(path string) io.WriteCloser {
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    logMaxSizeMB,
		MaxBackups: logMaxBackups,
		MaxAge:     logMaxAgeDays,
		Compress:   logCompress,
	}
}
