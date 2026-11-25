package service

import "github.com/blackpaw-studio/leo/internal/config"

// briefEnabled is brief.enabled in leo.yaml right now, read at each launch
// (launches are rare); an unreadable config reads as off.
func (sv *Supervisor) briefEnabled() bool {
	if sv.configPath == "" {
		return false
	}
	cfg, err := config.Load(sv.configPath)
	return err == nil && cfg.Brief.Enabled
}
