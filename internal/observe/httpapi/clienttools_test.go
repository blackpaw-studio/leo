package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

type fakeProvider struct {
	mu           sync.Mutex
	id           string
	disconnected []string
}

func (p *fakeProvider) Connect() string { return p.id }

func (p *fakeProvider) Disconnect(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disconnected = append(p.disconnected, id)
}

func (p *fakeProvider) gone() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.disconnected...)
}

// runStream serves one events stream over a real bus, calls publish after the
// hello is out, and returns the frames once the stream has ended.
func runStream(t *testing.T, provider Provider, publish func(*observe.Bus), frames int) string {
	t.Helper()
	bus := observe.NewBus()
	clock := &fakeClock{now: time.Unix(100, 0), ticker: &fakeTicker{ch: make(chan time.Time)}}
	w := &notifyingWriter{header: make(http.Header), flushed: make(chan struct{}, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ServeEvents(w, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx),
			EventsOptions{Source: bus, Clock: clock, Provider: provider})
		close(done)
	}()
	<-w.flushed // hello
	if publish != nil {
		publish(bus)
	}
	for i := 1; i < frames; i++ {
		select {
		case <-w.flushed:
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d never arrived; stream so far: %q", i, w.String())
		}
	}
	cancel()
	<-done
	return w.String()
}

// dataOf returns the decoded data payload of the stream's n-th frame.
func dataOf(t *testing.T, stream string, n int) map[string]any {
	t.Helper()
	frames := strings.Split(strings.TrimSpace(stream), "\n\n")
	if n >= len(frames) {
		t.Fatalf("frame %d missing in %q", n, stream)
	}
	var data string
	for _, line := range strings.Split(frames[n], "\n") {
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("frame %d: %v (%q)", n, err, data)
	}
	return m
}

func sampleRequest() observe.ClientToolRequest {
	return observe.ClientToolRequest{
		ID: "ctr_1", Agent: "alfred", Group: "browser", Tool: "browser_click",
		Args: json.RawMessage(`{"ref":"e1"}`), Owner: "ct_9", TimeoutS: 30,
		CreatedAt: time.Date(2026, 10, 26, 15, 21, 2, 0, time.UTC),
	}
}

func TestProviderStreamHelloCarriesClientID(t *testing.T) {
	stream := runStream(t, &fakeProvider{id: "ct_abc"}, nil, 1)
	if hello := dataOf(t, stream, 0); hello["client_id"] != "ct_abc" {
		t.Fatalf("hello = %v", hello)
	}
	plain := runStream(t, nil, nil, 1)
	if hello := dataOf(t, plain, 0); hello["client_id"] != nil {
		t.Fatalf("plain stream hello carries client_id: %v", hello)
	}
}

func TestProviderStreamGetsFullRequestAndCancel(t *testing.T) {
	stream := runStream(t, &fakeProvider{id: "ct_abc"}, func(b *observe.Bus) {
		b.Publish(observe.Event{Type: observe.EventClientToolRequest, Payload: &observe.ClientToolRequestPayload{Request: sampleRequest()}})
		b.Publish(observe.Event{Type: observe.EventClientToolCancelled, Payload: &observe.ClientToolCancelledPayload{ID: "ctr_1", Agent: "alfred", Reason: observe.CancelTimeout}})
	}, 3)
	req := dataOf(t, stream, 1)["request"].(map[string]any)
	if req["tool"] != "browser_click" || req["agent"] != "alfred" {
		t.Fatalf("provider request = %v", req)
	}
	if c := dataOf(t, stream, 2); c["agent"] != "alfred" || c["reason"] != "timeout" {
		t.Fatalf("provider cancelled = %v", c)
	}
}

func TestNonProviderStreamGetsRedactedRequestKeepingSeq(t *testing.T) {
	stream := runStream(t, nil, func(b *observe.Bus) {
		b.Publish(observe.Event{Type: observe.EventClientToolRequest, Payload: &observe.ClientToolRequestPayload{Request: sampleRequest()}})
		b.Publish(observe.Event{Type: observe.EventClientToolCancelled, Payload: &observe.ClientToolCancelledPayload{ID: "ctr_1", Agent: "alfred", Reason: observe.CancelTimeout}})
	}, 3)

	reqFrame := dataOf(t, stream, 1)
	if reqFrame["seq"] != float64(1) {
		t.Fatalf("redacted request lost its seq: %v", reqFrame)
	}
	if req := reqFrame["request"].(map[string]any); len(req) != 1 || req["id"] != "ctr_1" {
		t.Fatalf("non-provider stream saw %v, want only the id", req)
	}
	cancelFrame := dataOf(t, stream, 2)
	if cancelFrame["seq"] != float64(2) || cancelFrame["id"] != "ctr_1" || cancelFrame["agent"] != nil || cancelFrame["reason"] != nil {
		t.Fatalf("redacted cancelled = %v", cancelFrame)
	}
	if strings.Contains(stream, "browser_click") || strings.Contains(stream, "alfred") {
		t.Fatalf("redacted stream leaked the call: %q", stream)
	}
}

func TestChangedEventReachesEveryStream(t *testing.T) {
	stream := runStream(t, nil, func(b *observe.Bus) {
		b.Publish(observe.Event{Type: observe.EventClientToolsChanged, Payload: &observe.ClientToolsChangedPayload{Revision: 3, Tools: []string{"a"}}})
	}, 2)
	if got := dataOf(t, stream, 1); got["revision"] != float64(3) {
		t.Fatalf("changed = %v", got)
	}
}

func TestStreamCloseDisconnectsClient(t *testing.T) {
	p := &fakeProvider{id: "ct_abc"}
	runStream(t, p, nil, 1)
	if got := p.gone(); len(got) != 1 || got[0] != "ct_abc" {
		t.Fatalf("disconnected = %v", got)
	}
}
