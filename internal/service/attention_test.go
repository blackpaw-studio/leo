package service

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestSpawnedAgentViewCarriesAttentionOnlyWhenPresent(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.homePath = t.TempDir()
	store := observe.NewAttentionStore(nil)
	store.Set("with", observe.AttentionErrored)
	sv.SetAttention(store)

	with := sv.spawnedAgentView(daemon.AgentSpawnSpec{Name: "with"}, time.Now())
	without := sv.spawnedAgentView(daemon.AgentSpawnSpec{Name: "without"}, time.Now())

	if with.Attention == nil || *with.Attention != (observe.AgentAttention{State: observe.AttentionErrored, Revision: 1}) {
		t.Errorf("with.Attention = %+v", with.Attention)
	}
	if without.Attention != nil {
		t.Errorf("without.Attention = %+v, want absent", without.Attention)
	}
}

func TestWireObservabilitySharesAttentionStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()

	obs := wireObservability(ctx, sv, writeFakeTmuxScript(t))

	if obs.Attention == nil || sv.attentionStore() != obs.Attention {
		t.Fatal("supervisor not wired to the returned attention store")
	}
	events, unsub, _ := obs.Bus.Subscribe(4)
	defer unsub()
	obs.Attention.Set("a", observe.AttentionWorking)
	ev := <-events
	p, ok := ev.Payload.(*observe.AgentActivityPayload)
	if !ok || p.Attention == nil || p.Attention.State != observe.AttentionWorking {
		t.Fatalf("bus event = %+v", ev)
	}
}
