package drain

import (
	"errors"
	"testing"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// Fixture captures copied from internal/tmux/composer_test.go.
const (
	claudeEmptyCapture = `
────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────

  ⏵⏵ auto mode on (shift+tab to cycle) · ← 6 agents           ● high · /effort
`
	claudeBusyCapture = `
❯ Describe the history of the Roman Empire exhaustively.

✽ Grooving…

────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← 6 agents           ● high · /effort
`
	claudeDialogCapture = `
Claude Code needs permission to use this directory

❯ 1. Trust this folder
  2. Exit

  Enter to confirm · Esc to cancel
`
	codexBusyCapture = `
• Working (12s · esc to interrupt)

  gpt-5.6-luna default · ~/work
`
	codexEmptyCapture = `
› Ask Codex to do anything

  gpt-5.6-luna default · ~/work
`
)

func captureOf(s string) func() (string, error) {
	return func() (string, error) { return s, nil }
}

func TestClassify(t *testing.T) {
	errCapture := errors.New("no such pane")
	tests := []struct {
		name          string
		in            ProbeInput
		wantBusy      bool
		wantTransport string
		wantErr       bool
	}{
		{"bridge busy", ProbeInput{Name: "a", BridgeConnected: true, BridgeBusy: true}, true, TransportBridge, false},
		{"bridge idle ignores busy capture", ProbeInput{Name: "a", BridgeConnected: true, Capture: func() (string, error) {
			t.Error("Capture called for a connected bridge")
			return claudeBusyCapture, nil
		}}, false, TransportBridge, false},
		{"legacy claude busy", ProbeInput{Name: "a", Capture: captureOf(claudeBusyCapture)}, true, TransportComposer, false},
		{"legacy claude empty", ProbeInput{Name: "a", Harness: "claude", Capture: captureOf(claudeEmptyCapture)}, false, TransportComposer, false},
		{"legacy dialog is not busy", ProbeInput{Name: "a", Capture: captureOf(claudeDialogCapture)}, false, TransportComposer, false},
		{"capture error is not busy", ProbeInput{Name: "a", Capture: func() (string, error) { return "", errCapture }}, false, TransportComposer, true},
		{"codex uses codex classifier", ProbeInput{Name: "a", Harness: "codex", Capture: captureOf(codexBusyCapture)}, true, TransportComposer, false},
		{"codex empty", ProbeInput{Name: "a", Harness: "codex", Capture: captureOf(codexEmptyCapture)}, false, TransportComposer, false},
		{"unknown harness is not busy", ProbeInput{Name: "a", Harness: "mystery", Capture: captureOf(claudeBusyCapture)}, false, TransportComposer, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Classify(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got.Name != tt.in.Name || !got.Exists {
				t.Errorf("probe = %+v, want name %q and Exists", got, tt.in.Name)
			}
			if got.Busy != tt.wantBusy {
				t.Errorf("Busy = %v, want %v", got.Busy, tt.wantBusy)
			}
			if got.Transport != tt.wantTransport {
				t.Errorf("Transport = %q, want %q", got.Transport, tt.wantTransport)
			}
		})
	}
}

// Busy reads alike across harnesses, so the classifier choice shows in the
// empty composer only its own classifier recognizes.
func TestComposerClassifierByHarness(t *testing.T) {
	for _, tc := range []struct {
		harness string
		capture string
	}{
		{"", claudeEmptyCapture},
		{"claude", claudeEmptyCapture},
		{"codex", codexEmptyCapture},
	} {
		classify, err := composerClassifier(tc.harness)
		if err != nil {
			t.Fatalf("composerClassifier(%q): %v", tc.harness, err)
		}
		if got := classify(tc.capture); got != tmux.ComposerEmpty {
			t.Errorf("harness %q classified its own empty composer as %s", tc.harness, got)
		}
	}
}
