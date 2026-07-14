package picker

import (
	"context"
	"testing"
	"time"
)

func TestAgentZeroValue(t *testing.T) {
	a := Agent{Name: "x", Host: LocalHost, Status: "running", StartedAt: time.Now()}
	if a.AttachOnly {
		t.Fatalf("AttachOnly should default false")
	}
	if LocalHost != "local" {
		t.Fatalf("LocalHost = %q, want local", LocalHost)
	}
}

// staticBackend is a trivial Backend used to prove Run wires up and returns
// without a selection when the caller-cancelled context tears the program down.
type staticBackend struct{ agents []Agent }

func (s staticBackend) List(context.Context) ([]Agent, error) { return s.agents, nil }
func (staticBackend) Rename(context.Context, string, string) error { return nil }
func (staticBackend) Stop(context.Context, string) error           { return nil }
func (staticBackend) Suspend(context.Context, string) error        { return nil }
func (staticBackend) Resume(context.Context, string) error         { return nil }

func TestBackendInterfaceSatisfied(t *testing.T) {
	var _ Backend = staticBackend{}
}
