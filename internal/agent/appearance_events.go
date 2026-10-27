package agent

import (
	"errors"
	"fmt"
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

// InvalidAppearanceError is a manual emoji or color that fails validation.
// It is the caller's mistake, so API layers map it to 400.
type InvalidAppearanceError struct {
	Field  string // "emoji" or "color"
	Reason string
}

func (e *InvalidAppearanceError) Error() string { return e.Reason }

// SetAppearance patches name's manual emoji and color (see
// agentstore.AppearancePatch), publishes agent_appearance_changed, and
// returns the agent's resolved appearance (nil when nothing is set). name is
// the agent's exact name; an agent with no record is an *ErrNotFound.
func (m *Manager) SetAppearance(name string, patch agentstore.AppearancePatch) (*observe.Appearance, error) {
	if err := validateAppearancePatch(patch); err != nil {
		return nil, err
	}
	cfg, err := m.cfgLoader()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if _, err := agentstore.SetAppearance(cfg.HomePath, name, patch); err != nil {
		if errors.Is(err, agentstore.ErrNotFound) {
			return nil, &ErrNotFound{Query: name}
		}
		return nil, fmt.Errorf("saving appearance: %w", err)
	}
	records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return nil, fmt.Errorf("loading agent records: %w", err)
	}
	rec := records[name]
	resolved := AgentAppearance(cfg, rec.Name, rec.Template, rec.Environments, rec.Appearance)
	m.publish(observe.Event{
		Type:    observe.EventAgentAppearanceChanged,
		Payload: &observe.AgentAppearanceChangedPayload{Agent: name, Appearance: resolved},
	})
	return resolved, nil
}

// validateAppearancePatch checks every value the patch sets; "" clears and
// needs no check.
func validateAppearancePatch(patch agentstore.AppearancePatch) error {
	if patch.Emoji != nil && *patch.Emoji != "" {
		if err := config.ValidateAppearanceEmoji(*patch.Emoji); err != nil {
			return &InvalidAppearanceError{Field: "emoji", Reason: err.Error()}
		}
	}
	if patch.Color != nil && *patch.Color != "" {
		if err := config.ValidateAppearanceColor(*patch.Color); err != nil {
			return &InvalidAppearanceError{Field: "color", Reason: err.Error()}
		}
	}
	return nil
}
