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

type EventsOptions struct {
	Source       EventSource
	Heartbeat    time.Duration
	WriteTimeout time.Duration
	Buffer       int
	Clock        Clock
}

// ServeEvents streams the local event source. Every connection starts with a
// hello frame, followed by named event frames and periodic comment heartbeats.
func ServeEvents(w http.ResponseWriter, r *http.Request, opts EventsOptions) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	clock := opts.Clock
	if clock == nil {
		clock = systemClock{}
	}
	rc := http.NewResponseController(w)
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

	now := clock.Now()
	deadline()
	w.WriteHeader(http.StatusOK)
	if WriteEvent(w, string(observe.EventHello), observe.HelloPayload{
		Meta: observe.Meta{Seq: seq, At: now}, Version: observe.SnapshotVersion, ServerTime: now,
	}) != nil || rc.Flush() != nil {
		return
	}

	heartbeat := opts.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 20 * time.Second
	}
	t := clock.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C():
			deadline()
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			deadline()
			if WriteEvent(w, string(ev.Type), ev.Payload) != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

// ServeState shares snapshot production and envelope/error adaptation between
// the TCP web API and Unix-socket API.
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
