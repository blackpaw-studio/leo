package config

// Attention states a push notification can be filtered on. They match the
// wire values of observe.AttentionState.
const (
	PushStateNeedsInput = "needs_input"
	PushStateFinished   = "finished"
	PushStateErrored    = "errored"
	// PushStateChat is not an attention state: it filters Leo Chat pushes
	// for an agent's outbound messages.
	PushStateChat = "chat"
)

// PushConfig chooses which attention transitions push to paired iOS devices.
// Each state is its own optional bool and cascades on its own: template,
// then defaults, then on.
type PushConfig struct {
	NeedsInput *bool `yaml:"needs_input,omitempty"`
	Finished   *bool `yaml:"finished,omitempty"`
	Errored    *bool `yaml:"errored,omitempty"`
	Chat       *bool `yaml:"chat,omitempty"`
}

// state returns the setting for an attention state; nil when unset or the
// state is not filterable.
func (p PushConfig) state(state string) *bool {
	switch state {
	case PushStateNeedsInput:
		return p.NeedsInput
	case PushStateFinished:
		return p.Finished
	case PushStateErrored:
		return p.Errored
	case PushStateChat:
		return p.Chat
	}
	return nil
}

// PushEnabled reports whether an agent spawned from template should push
// when it enters state. Cascade: template → defaults → on. An empty or
// unknown template resolves from defaults.
func (c *Config) PushEnabled(template, state string) bool {
	if tmpl, ok := c.Templates[template]; ok {
		if v := tmpl.Push.state(state); v != nil {
			return *v
		}
	}
	if v := c.Defaults.Push.state(state); v != nil {
		return *v
	}
	return true
}
