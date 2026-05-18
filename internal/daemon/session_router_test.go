package daemon

import (
	"testing"
	"time"
)

func TestSessionRouterEnqueueAccepts(t *testing.T) {
	r := newSessionRouter()
	inv, ok := r.Enqueue(EnqueueParams{
		Session:  "leo-session-foo",
		Task:     "morning",
		Prompt:   "do the thing",
		Channels: []string{"plugin:slack@official"},
		QueueMax: 5,
		Timeout:  10 * time.Second,
	})
	if !ok {
		t.Fatalf("expected enqueue accepted")
	}
	if inv.ID == "" {
		t.Fatalf("expected non-empty invocation id")
	}
	if inv.Task != "morning" {
		t.Fatalf("task mismatch: %q", inv.Task)
	}
}

func TestSessionRouterEnqueueQueueFull(t *testing.T) {
	r := newSessionRouter()
	p := EnqueueParams{Session: "s", Task: "t", Prompt: "x", QueueMax: 2, Timeout: time.Second}
	if _, ok := r.Enqueue(p); !ok {
		t.Fatal("first enqueue should accept")
	}
	if _, ok := r.Enqueue(p); !ok {
		t.Fatal("second enqueue should accept (queue depth 2)")
	}
	if _, ok := r.Enqueue(p); ok {
		t.Fatal("third enqueue should reject (queue full)")
	}
}

func TestSessionRouterLookupByID(t *testing.T) {
	r := newSessionRouter()
	inv, _ := r.Enqueue(EnqueueParams{Session: "s", Task: "t", Prompt: "x", QueueMax: 5, Timeout: time.Second})
	got, ok := r.Lookup(inv.ID)
	if !ok || got.Task != "t" {
		t.Fatalf("lookup failed: %+v ok=%v", got, ok)
	}
	if _, ok := r.Lookup("does-not-exist"); ok {
		t.Fatalf("expected miss for unknown id")
	}
}
