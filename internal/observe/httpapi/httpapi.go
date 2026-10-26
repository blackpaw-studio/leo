package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// bootID identifies this daemon process on every SSE hello. It is minted
// once at package init: anything that restarts with the process (attention
// revisions, event seq) is scoped to it.
var bootID = newBootID()

func newBootID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; a time-derived id
		// still changes across restarts, which is all consumers rely on.
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// BootID returns this process's boot id.
func BootID() string { return bootID }

// Provider admits a client-tools provider stream (`?client_tools=1`): the
// stream is given a client id for the length of its connection, and the
// relay withdraws that client's tools when the stream ends.
type Provider interface {
	Connect() string
	Disconnect(id string)
}

type EventsOptions struct {
	// BootID overrides the process boot id reported on hello (tests).
	BootID       string
	Source       EventSource
	Heartbeat    time.Duration
	WriteTimeout time.Duration
	Buffer       int
	Clock        Clock
	// Provider, when non-nil, makes this a client-tools provider stream: the
	// hello carries its client_id and it receives client_tool.* events in
	// full. Every other stream sees them redacted to ids.
	Provider Provider
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

	var clientID string
	if opts.Provider != nil {
		clientID = opts.Provider.Connect()
		defer opts.Provider.Disconnect(clientID)
	}

	boot := opts.BootID
	if boot == "" {
		boot = bootID
	}
	now := clock.Now()
	deadline()
	w.WriteHeader(http.StatusOK)
	if WriteEvent(w, string(observe.EventHello), observe.HelloPayload{
		Meta: observe.Meta{Seq: seq, At: now}, Version: observe.SnapshotVersion, ServerTime: now, BootID: boot, Features: observe.Features(), ClientID: clientID,
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
			if WriteEvent(w, string(ev.Type), payloadFor(ev, opts.Provider != nil)) != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

// payloadFor is what one stream is allowed to see of ev: client-tool calls
// carry an agent's arguments, so only provider streams get them whole. The
// others get the id alone, which keeps their seq contiguous.
func payloadFor(ev observe.Event, provider bool) any {
	if provider {
		return ev.Payload
	}
	switch p := ev.Payload.(type) {
	case *observe.ClientToolRequestPayload:
		return p.Redacted()
	case *observe.ClientToolCancelledPayload:
		return p.Redacted()
	}
	return ev.Payload
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
