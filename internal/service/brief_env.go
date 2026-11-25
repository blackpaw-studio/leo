package service

import (
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
	"github.com/blackpaw-studio/leo/internal/config"
)

// briefEnabled is brief.enabled in leo.yaml right now, read at each launch
// (launches are rare); an unreadable config reads as off.
func (sv *Supervisor) briefEnabled() bool {
	if sv.configPath == "" {
		return false
	}
	cfg, err := config.Load(sv.configPath)
	return err == nil && cfg.Brief.Enabled
}

// storedBrief is name's stored brief while briefs are on, nil otherwise: what
// an agent_spawned announcement carries, so a stream consumer that learns of
// the agent from it (a respawn, a live rename) does not lose its brief.
// Redaction for readers that may not see content is applied downstream, on
// the stream, like every other field of the agent.
func (sv *Supervisor) storedBrief(name string) *brief.Brief {
	if !sv.briefEnabled() {
		return nil
	}
	return agentstore.BriefOf(sv.homePath, name)
}
