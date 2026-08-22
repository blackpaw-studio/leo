package picker

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
)

func (staticBackend) Templates(context.Context) ([]string, error)          { return nil, nil }
func (staticBackend) SwitchTemplate(context.Context, string, string) error { return nil }

func TestAgentZeroValue(t *testing.T) {
	a := Agent{Name: "x", Host: LocalHost, Status: "running", StartedAt: time.Now()}
	if a.AttachOnly {
		t.Fatalf("AttachOnly should default false")
	}
	if LocalHost != "local" {
		t.Fatalf("LocalHost = %q, want local", LocalHost)
	}
}

// staticBackend is a trivial Backend used to prove a concrete type satisfies
// the Backend interface without pulling in any real I/O.
type staticBackend struct{ agents []Agent }

func (s staticBackend) List(context.Context) ([]Agent, error)      { return s.agents, nil }
func (staticBackend) Rename(context.Context, string, string) error { return nil }
func (staticBackend) Stop(context.Context, string) error           { return nil }
func (staticBackend) Start(context.Context, string) error          { return nil }
func (staticBackend) DeletePlan(context.Context, string) (agent.DeletePlan, error) {
	return agent.DeletePlan{}, nil
}
func (staticBackend) Delete(context.Context, string, bool) error { return nil }

func TestBackendInterfaceSatisfied(t *testing.T) {
	var _ Backend = staticBackend{}
}
