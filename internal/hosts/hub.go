package hosts

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

type Hub struct {
	mu             sync.RWMutex
	conns          map[string]*connection
	defaultHost    string
	bus            *observe.Bus
	local          http.Handler
	stateDir       string
	connectTimeout time.Duration
	deps           dependencies
}

func New(cfg *config.Config, bus *observe.Bus, local http.Handler) *Hub {
	defaultHost := cfg.Client.DefaultHost
	if defaultHost == "" && len(cfg.Client.Hosts) > 0 {
		names := make([]string, 0, len(cfg.Client.Hosts))
		for name := range cfg.Client.Hosts {
			names = append(names, name)
		}
		sort.Strings(names)
		defaultHost = names[0]
	}
	h := &Hub{conns: map[string]*connection{}, defaultHost: defaultHost, bus: bus, local: local, stateDir: filepath.Join(cfg.StatePath(), "remotes"), connectTimeout: 20 * time.Second, deps: realDependencies()}
	for name, hc := range cfg.Client.Hosts {
		h.conns[name] = newConnection(h, name, hc, cfg.HostForwardSocket(name), cfg.HostControlPath(name))
	}
	return h
}

func (h *Hub) Start(ctx context.Context) {
	h.mu.RLock()
	cs := make([]*connection, 0, len(h.conns))
	for _, c := range h.conns {
		if c.cfg.Autoconnect {
			cs = append(cs, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range cs {
		_ = c.connect(ctx, true)
	}
}

func (h *Hub) Rows() []Row {
	localDefault := h.defaultHost == "" && len(h.conns) == 0
	rows := []Row{{Name: config.LocalhostSentinel, Local: true, Default: localDefault, State: "local"}}
	names := make([]string, 0, len(h.conns))
	for n := range h.conns {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rows = append(rows, h.conns[n].row(n == h.defaultHost))
	}
	return rows
}
func (h *Hub) Row(name string) (Row, bool) {
	if name == config.LocalhostSentinel {
		return h.Rows()[0], true
	}
	h.mu.RLock()
	c, ok := h.conns[name]
	h.mu.RUnlock()
	if !ok {
		return Row{}, false
	}
	return c.row(name == h.defaultHost), true
}
func (h *Hub) Connect(ctx context.Context, name string) (Row, error) {
	h.mu.RLock()
	c, ok := h.conns[name]
	h.mu.RUnlock()
	if !ok {
		return Row{}, &HostError{Code: "host_unknown", Message: "unknown host " + name}
	}
	if err := c.connect(ctx, true); err != nil {
		return Row{}, err
	}
	return c.row(name == h.defaultHost), nil
}
func (h *Hub) Disconnect(name string) (Row, error) {
	h.mu.RLock()
	c, ok := h.conns[name]
	h.mu.RUnlock()
	if !ok {
		return Row{}, &HostError{Code: "host_unknown", Message: "unknown host " + name}
	}
	if err := c.disconnect(context.Background()); err != nil {
		return Row{}, err
	}
	return c.row(name == h.defaultHost), nil
}
func (h *Hub) Close() error {
	h.mu.RLock()
	cs := make([]*connection, 0, len(h.conns))
	for _, c := range h.conns {
		cs = append(cs, c)
	}
	h.mu.RUnlock()
	for _, c := range cs {
		_ = c.close()
	}
	return nil
}
func (h *Hub) ensure(ctx context.Context, name string) (*connection, error) {
	h.mu.RLock()
	c, ok := h.conns[name]
	h.mu.RUnlock()
	if !ok {
		return nil, &HostError{Code: "host_unknown", Message: "unknown host " + name}
	}
	if err := c.connect(ctx, false); err != nil {
		return nil, err
	}
	timeout := h.deps.clock.After(h.connectTimeout)
	for {
		state, changed, closed := c.snapshot()
		if closed {
			return nil, ErrClosed
		}
		if state == StateConnected {
			return c, nil
		}
		if state == StateError {
			return nil, &HostError{Code: c.row(false).Code, Message: c.row(false).Error}
		}
		select {
		case <-changed:
			continue
		case <-timeout:
			return nil, &HostError{Code: "host_unavailable", Message: fmt.Sprintf("host %s unavailable", name)}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
func (h *Hub) publish(name string, r Row) {
	if h.bus == nil {
		return
	}
	fields := map[string]any{"host": name, "state": r.State}
	if r.Error != "" {
		fields["error"] = r.Error
	}
	if r.Code != "" {
		fields["code"] = r.Code
	}
	h.bus.Publish(observe.Event{Type: observe.EventHostStateChanged, Payload: &observe.RawPayload{Fields: fields}})
}
func removeSockets(paths ...string) {
	for _, p := range paths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
}
