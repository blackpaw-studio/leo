package agent

import (
	"log"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// RepublishAppearance publishes agent_appearance_changed for every known
// agent, live or dormant, against current config and records. It keeps no
// diff cache (agent counts are small): consumers apply the value as-is. The
// daemon calls it after every config reload, which every rules write goes
// through.
func (m *Manager) RepublishAppearance() {
	if m.publisher == nil {
		return
	}
	cfg, err := m.cfgLoader()
	if err != nil {
		log.Printf("appearance: republish skipped, config unavailable: %v", err)
		return
	}
	for _, rec := range m.List() {
		m.publishAppearance(cfg, rec.Name, rec.Template, rec.Environments, rec.ManualAppearance)
	}
}

// publishAppearance announces one agent's resolved appearance.
func (m *Manager) publishAppearance(cfg *config.Config, name, template string, override []string, manual *agentstore.Appearance) {
	m.publish(observe.Event{
		Type: observe.EventAgentAppearanceChanged,
		Payload: &observe.AgentAppearanceChangedPayload{
			Agent:      name,
			Appearance: AgentAppearance(cfg, name, template, override, manual),
		},
	})
}

// publishRecordAppearance is publishAppearance for a stored record.
func (m *Manager) publishRecordAppearance(cfg *config.Config, rec agentstore.Record) {
	m.publishAppearance(cfg, rec.Name, rec.Template, rec.Environments, rec.Appearance)
}
