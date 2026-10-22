package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPostFromClientStoresAnOutboundMessageWithoutDelivering(t *testing.T) {
	f := newFixture(t)
	m, err := f.svc.PostFromClient(context.Background(), ClientPostRequest{Agent: "assistant", Client: "pipe", Text: "  cam offline  "})
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "assistant" || m.Direction != DirectionOut || m.Kind != KindText || m.Text != "cam offline" || m.Sender != "client:pipe" {
		t.Fatalf("message = %+v", m)
	}
	stored, err := f.store.Message(m.ID)
	if err != nil || stored.Sender != "client:pipe" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	if got := f.pub.types(); len(got) != 1 || got[0] != EventMessage {
		t.Fatalf("events = %v, want exactly one chat.message", got)
	}
	if got := f.pub.waitMessage(t); got.ID != m.ID || got.Sender != "client:pipe" {
		t.Fatalf("published = %+v", got)
	}
	if len(f.deliver.refs) != 0 {
		t.Fatalf("a client post woke the agent: %v", f.deliver.refs)
	}
}

func TestPostFromClientRejectsUnknownAgentAndBadText(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.PostFromClient(ctx, ClientPostRequest{Agent: "ghost", Client: "pipe", Text: "hi"}); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent err = %v", err)
	}
	if _, err := f.svc.PostFromClient(ctx, ClientPostRequest{Agent: "assistant", Client: "pipe", Text: " \n "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank text err = %v", err)
	}
	long := strings.Repeat("x", MaxTextBytes+1)
	if _, err := f.svc.PostFromClient(ctx, ClientPostRequest{Agent: "assistant", Client: "pipe", Text: long}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized text err = %v", err)
	}
	if got := f.thread(t, "assistant"); len(got) != 0 {
		t.Fatalf("rejected posts left messages: %+v", got)
	}
}
