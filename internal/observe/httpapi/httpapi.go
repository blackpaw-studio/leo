package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

type EventSource interface {
	Subscribe(int) (<-chan observe.Event, func(), uint64)
}
type Ticker interface {
	C() <-chan time.Time
	Stop()
}
type Clock interface {
	Now() time.Time
	NewTicker(time.Duration) Ticker
}
type systemClock struct{}

func (systemClock) Now() time.Time                   { return time.Now() }
func (systemClock) NewTicker(d time.Duration) Ticker { return ticker{time.NewTicker(d)} }

type ticker struct{ *time.Ticker }

func (t ticker) C() <-chan time.Time { return t.Ticker.C }

type Frame struct {
	Event   string
	Payload any
}
type EventsOptions struct {
	Source       EventSource
	Hello        func(uint64, time.Time) any
	Initial      func() ([]Frame, uint64)
	Heartbeat    time.Duration
	WriteTimeout time.Duration
	Buffer       int
	Accept       func(observe.Event) bool
	Payload      func(observe.Event) any
	Clock        Clock
	Covered      map[observe.EventType]bool
}

func ServeEvents(w http.ResponseWriter, r *http.Request, opts EventsOptions) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	rc := http.NewResponseController(w)
	clock := opts.Clock
	if clock == nil {
		clock = systemClock{}
	}
	deadline := func() {
		if opts.WriteTimeout > 0 {
			_ = rc.SetWriteDeadline(clock.Now().Add(opts.WriteTimeout))
		}
	}
	var events <-chan observe.Event
	var unsubscribe func()
	var seq uint64
	if opts.Source != nil {
		buffer := opts.Buffer
		if buffer <= 0 {
			buffer = 32
		}
		events, unsubscribe, seq = opts.Source.Subscribe(buffer)
		defer unsubscribe()
	}
	deadline()
	w.WriteHeader(http.StatusOK)
	now := clock.Now()
	var initial []Frame
	snapshotSeq := seq
	if opts.Initial != nil {
		initial, snapshotSeq = opts.Initial()
	}
	if opts.Hello != nil {
		deadline()
		if WriteEvent(w, "hello", opts.Hello(seq, now)) != nil {
			return
		}
	}
	if opts.Initial != nil {
		for _, frame := range initial {
			deadline()
			if WriteEvent(w, frame.Event, frame.Payload) != nil {
				return
			}
		}
	}
	if rc.Flush() != nil {
		return
	}
	heartbeat := opts.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 20 * time.Second
	}
	ticker := clock.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C():
			deadline()
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			if (opts.Initial != nil && opts.Covered[ev.Type] && eventSeq(ev) <= snapshotSeq) || (opts.Accept != nil && !opts.Accept(ev)) {
				continue
			}
			deadline()
			payload := any(ev.Payload)
			if opts.Payload != nil {
				payload = opts.Payload(ev)
			}
			if WriteEvent(w, string(ev.Type), payload) != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
	}
}

func eventSeq(ev observe.Event) uint64 {
	b, _ := json.Marshal(ev.Payload)
	var m struct {
		Seq uint64 `json:"seq"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Seq
}

type HostPayload struct {
	Payload any
	Host    string
}

func (p HostPayload) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(p.Payload)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if _, ok := m["host"]; !ok {
		m["host"] = p.Host
	}
	return json.Marshal(m)
}

func ServeState(w http.ResponseWriter, r *http.Request, produce func(context.Context) (any, error), write func(http.ResponseWriter, int, any, error)) {
	data, err := produce(r.Context())
	if err != nil {
		write(w, http.StatusInternalServerError, nil, err)
		return
	}
	write(w, http.StatusOK, data, nil)
}

func WriteEvent(w io.Writer, event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}
