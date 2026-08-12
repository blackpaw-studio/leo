package picker

import (
	"context"
	"errors"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// The picker is a second door onto `leo agent set-template`, so the policy the
// CLI verb enforces has to hold here too: a refused template must never reach
// the daemon.
func TestLocalBackendSwitchTemplateRefusedByPolicy(t *testing.T) {
	var reached bool
	b := NewLocalBackend("/home", LocalPolicy{
		CanSwitchTo: func(template string) error {
			return errors.New("not permitted to spawn template " + template)
		},
	})
	b.switchTo = func(context.Context, string, string, string) (agent.SwitchResult, error) {
		reached = true
		return agent.SwitchResult{}, nil
	}

	err := b.SwitchTemplate(context.Background(), "leo-x", "codex")
	if err == nil {
		t.Fatal("expected the policy refusal to be returned")
	}
	if reached {
		t.Error("a refused switch still called the daemon")
	}
}

func TestLocalBackendSwitchTemplateAllowedByPolicy(t *testing.T) {
	var gotName, gotTemplate string
	b := NewLocalBackend("/home", LocalPolicy{
		CanSwitchTo: func(string) error { return nil },
	})
	b.switchTo = func(_ context.Context, workDir, name, template string) (agent.SwitchResult, error) {
		gotName, gotTemplate = name, template
		return agent.SwitchResult{Name: name}, nil
	}

	if err := b.SwitchTemplate(context.Background(), "leo-x", "codex"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	if gotName != "leo-x" || gotTemplate != "codex" {
		t.Errorf("daemon called with (%q, %q), want (leo-x, codex)", gotName, gotTemplate)
	}
}

// A zero policy is what a caller that supplies neither templates nor a gate
// gets: the menu reports that templates are unavailable rather than opening an
// empty chooser.
func TestLocalBackendTemplatesUnavailable(t *testing.T) {
	b := NewLocalBackend("/home", LocalPolicy{})
	if _, err := b.Templates(context.Background()); err == nil {
		t.Fatal("expected an error when no template source is configured")
	}
}
