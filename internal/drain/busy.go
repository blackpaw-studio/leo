package drain

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// ProbeInput is what the daemon knows about one existing agent.
type ProbeInput struct {
	Name            string
	BridgeConnected bool
	BridgeBusy      bool
	// Harness is the agent's harness adapter name; "" means claude.
	Harness string
	// Capture reads the agent's pane; only called without a connected
	// bridge.
	Capture func() (string, error)
}

// Classify decides whether one agent is busy. A connected bridge's busy
// flag is authoritative. Otherwise the pane capture goes through the
// harness's composer classifier, and only ComposerBusy counts: a dialog
// (ComposerUnknown) or a failed capture is not busy, and the error is
// returned for logging.
func Classify(in ProbeInput) (Probe, error) {
	if in.BridgeConnected {
		return Probe{Name: in.Name, Exists: true, Busy: in.BridgeBusy, Transport: TransportBridge}, nil
	}
	probe := Probe{Name: in.Name, Exists: true, Transport: TransportComposer}
	classify, err := composerClassifier(in.Harness)
	if err != nil {
		return probe, err
	}
	if in.Capture == nil {
		return probe, fmt.Errorf("agent %q: no pane capture", in.Name)
	}
	capture, err := in.Capture()
	if err != nil {
		return probe, fmt.Errorf("agent %q: capture pane: %w", in.Name, err)
	}
	probe.Busy = classify(capture) == tmux.ComposerBusy
	return probe, nil
}

func composerClassifier(harness string) (tmux.ComposerClassifier, error) {
	switch harness {
	case "", "claude":
		return tmux.ClaudeComposerClassifier, nil
	case "codex":
		return tmux.CodexComposerClassifier, nil
	case "opencode":
		return tmux.OpenCodeComposerClassifier, nil
	default:
		return nil, fmt.Errorf("no composer classifier for harness %q", harness)
	}
}
