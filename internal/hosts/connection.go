package hosts

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

type connectionCommand struct {
	kind     string
	explicit bool
	ack      chan error
	attempt  uint64
}
type connectionUpdate struct {
	state   State
	err     *HostError
	attempt uint64
}

type connection struct {
	mu             sync.RWMutex
	hub            *Hub
	name           string
	cfg            config.HostConfig
	localSock, ctl string
	state          State
	err, code      string
	connectedAt    *time.Time
	changed        chan struct{}
	closed         bool
	commands       chan connectionCommand
	updates        chan connectionUpdate
	supervisorDone chan struct{}
	transport      *http.Transport
	attempt        uint64
}

func newConnection(h *Hub, n string, c config.HostConfig, sock, ctl string) *connection {
	cn := &connection{hub: h, name: n, cfg: c, localSock: sock, ctl: ctl, state: StateDisconnected, changed: make(chan struct{}), commands: make(chan connectionCommand), updates: make(chan connectionUpdate, 32), supervisorDone: make(chan struct{})}
	cn.transport = &http.Transport{IdleConnTimeout: 30 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	go cn.supervise()
	return cn
}
func (c *connection) stateValue() State                { c.mu.RLock(); defer c.mu.RUnlock(); return c.state }
func (c *connection) transition(s State, e *HostError) { c.setState(s, e) }
func (c *connection) start(ctx context.Context)        { _ = c.connect(ctx, true) }
func (c *connection) row(def bool) Row {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Row{Name: c.name, SSH: c.cfg.SSH, Default: def, State: c.state, Error: c.err, Code: c.code, ConnectedAt: c.connectedAt}
}
func (c *connection) snapshot() (State, <-chan struct{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state, c.changed, c.closed
}
func (c *connection) attemptValue() uint64 { c.mu.RLock(); defer c.mu.RUnlock(); return c.attempt }
func (c *connection) setState(s State, he *HostError) {
	c.mu.Lock()
	if c.closed && s != StateDisconnected {
		c.mu.Unlock()
		return
	}
	if c.state == s && he == nil {
		c.mu.Unlock()
		return
	}
	c.state = s
	c.err = ""
	c.code = ""
	if he != nil {
		c.err = he.Message
		c.code = he.Code
	}
	switch s {
	case StateConnected:
		now := c.hub.deps.clock.Now()
		c.connectedAt = &now
	case StateConnecting:
		c.connectedAt = nil
	}
	close(c.changed)
	c.changed = make(chan struct{})
	row := Row{Name: c.name, SSH: c.cfg.SSH, State: c.state, Error: c.err, Code: c.code, ConnectedAt: c.connectedAt}
	c.mu.Unlock()
	c.hub.publish(c.name, row)
}
func (c *connection) send(ctx context.Context, cmd connectionCommand) error {
	select {
	case c.commands <- cmd:
	case <-c.supervisorDone:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-cmd.ack:
		return err
	case <-c.supervisorDone:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *connection) connect(ctx context.Context, explicit bool) error {
	return c.send(ctx, connectionCommand{kind: "connect", explicit: explicit, ack: make(chan error, 1)})
}
func (c *connection) disconnect(ctx context.Context) error {
	return c.send(ctx, connectionCommand{kind: "disconnect", ack: make(chan error, 1)})
}
func (c *connection) close() error {
	err := c.send(context.Background(), connectionCommand{kind: "close", ack: make(chan error, 1)})
	<-c.supervisorDone
	return err
}
func (c *connection) markDown(attempt uint64) {
	_ = c.send(context.Background(), connectionCommand{kind: "down", attempt: attempt, ack: make(chan error, 1)})
}

func (c *connection) supervise() {
	defer close(c.supervisorDone)
	var cancel context.CancelFunc
	var workerDone <-chan struct{}
	stop := func() {
		if cancel != nil {
			cancel()
			<-workerDone
			cancel = nil
			workerDone = nil
		}
		ctx, x := context.WithTimeout(context.Background(), 2*time.Second)
		c.hub.deps.exit(ctx, c.cfg, c.ctl)
		x()
		if c.transport != nil {
			c.transport.CloseIdleConnections()
		}
		c.hub.deps.remove(c.localSock, c.ctl)
		c.setState(StateDisconnected, nil)
	}
	start := func() {
		c.mu.Lock()
		c.attempt++
		attempt := c.attempt
		c.mu.Unlock()
		ctx, x := context.WithCancel(context.Background()) // #nosec G118 -- supervisor stores and calls x on disconnect/close
		cancel = x
		done := make(chan struct{})
		workerDone = done
		c.setState(StateConnecting, nil)
		go func() { defer close(done); c.run(ctx, attempt) }()
	}
	for {
		select {
		case u := <-c.updates:
			if u.attempt == c.attemptValue() {
				c.setState(u.state, u.err)
			}
		case cmd := <-c.commands:
			switch cmd.kind {
			case "connect":
				state, _, closed := c.snapshot()
				if closed {
					cmd.ack <- ErrClosed
					continue
				}
				if state == StateError && !cmd.explicit {
					cmd.ack <- nil
					continue
				}
				if state == StateError && cmd.explicit && cancel != nil {
					cancel()
					<-workerDone
					cancel = nil
					workerDone = nil
				}
				if cancel == nil {
					start()
				}
				cmd.ack <- nil
			case "disconnect":
				c.mu.Lock()
				c.attempt++
				c.mu.Unlock()
				stop()
				cmd.ack <- nil
			case "down":
				if cmd.attempt != c.attemptValue() {
					cmd.ack <- nil
					continue
				}
				c.mu.Lock()
				c.attempt++
				generation := c.attempt
				c.mu.Unlock()
				if cancel != nil {
					cancel()
					<-workerDone
					cancel = nil
					workerDone = nil
				}
				c.setState(StateDisconnected, &HostError{Code: "host_unavailable", Message: "forward unavailable"})
				go func() {
					if waitCtx(context.Background(), c.hub.deps.clock.After(time.Second)) {
						_ = c.send(context.Background(), connectionCommand{kind: "reconnect", attempt: generation, ack: make(chan error, 1)})
					}
				}()
				cmd.ack <- nil
			case "reconnect":
				if cmd.attempt == c.attemptValue() && cancel == nil {
					start()
				}
				cmd.ack <- nil
			case "close":
				c.mu.Lock()
				c.closed = true
				c.attempt++
				c.mu.Unlock()
				stop()
				cmd.ack <- nil
				return
			}
		}
	}
}
func (c *connection) emit(ctx context.Context, attempt uint64, s State, e *HostError) bool {
	select {
	case c.updates <- connectionUpdate{state: s, err: e, attempt: attempt}:
		return true
	case <-ctx.Done():
		return false
	}
}
func (c *connection) run(ctx context.Context, attempt uint64) {
	if err := os.MkdirAll(filepath.Dir(c.localSock), 0700); err != nil {
		c.emit(ctx, attempt, StateError, &HostError{Code: "ssh_failed", Message: err.Error()})
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		remote, err := c.hub.deps.discover(ctx, c.cfg.SSH, c.cfg.SSHArgs, c.ctl)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			he := classifyRunError(err)
			if terminal(he) {
				c.emit(ctx, attempt, StateError, remediate(c.cfg, he))
				return
			}
			c.emit(ctx, attempt, StateDisconnected, he)
			if !waitCtx(ctx, c.hub.deps.clock.After(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			c.emit(ctx, attempt, StateConnecting, nil)
			continue
		}
		healthyAt, err := c.runAttempt(ctx, attempt, remote)
		if ctx.Err() != nil {
			return
		}
		he := classifyRunError(err)
		if terminal(he) {
			c.emit(ctx, attempt, StateError, remediate(c.cfg, he))
			return
		}
		c.emit(ctx, attempt, StateDisconnected, he)
		if !healthyAt.IsZero() && c.hub.deps.clock.Now().Sub(healthyAt) >= time.Minute {
			backoff = time.Second
		}
		if !waitCtx(ctx, c.hub.deps.clock.After(backoff)) {
			return
		}
		backoff = nextBackoff(backoff)
		c.emit(ctx, attempt, StateConnecting, nil)
	}
}
func (c *connection) runAttempt(ctx context.Context, attempt uint64, remote string) (time.Time, error) {
	proc, err := c.hub.deps.start(ctx, c.cfg, c.ctl, c.localSock, remote)
	if err != nil {
		return time.Time{}, err
	}
	poll := c.hub.deps.clock.NewTicker(250 * time.Millisecond)
	defer func() { poll.Stop() }()
	deadline := c.hub.deps.clock.After(20 * time.Second)
	var healthyAt time.Time
	healthTick := poll.C()
	var fanCancel context.CancelFunc
	var fanDone chan struct{}
	defer func() {
		if fanCancel != nil {
			fanCancel()
			<-fanDone
		}
	}()
	for {
		select {
		case err := <-proc.Done():
			if proc.Stderr() != "" {
				return healthyAt, ClassifySSHStderr(proc.Stderr())
			}
			return healthyAt, err
		case <-ctx.Done():
			proc.Kill()
			<-proc.Done()
			return healthyAt, ctx.Err()
		case <-deadline:
			if healthyAt.IsZero() {
				proc.Kill()
				<-proc.Done()
				return healthyAt, &HostError{Code: "ssh_unreachable", Message: "health check timed out"}
			}
		case <-healthTick:
			if c.hub.deps.health(c.localSock) {
				if healthyAt.IsZero() {
					healthyAt = c.hub.deps.clock.Now()
					c.emit(ctx, attempt, StateConnected, nil)
					poll.Stop()
					poll = c.hub.deps.clock.NewTicker(15 * time.Second)
					healthTick = poll.C()
					fanCtx, x := context.WithCancel(ctx)
					fanCancel = x
					fanDone = make(chan struct{})
					go func() {
						defer close(fanDone)
						c.fanInLoop(fanCtx)
					}()
				}
			} else if !healthyAt.IsZero() {
				proc.Kill()
			}
		}
	}
}
func health(sock string) bool {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: time.Second}
	resp, err := cl.Get("http://daemon/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}
func classifyRunError(err error) *HostError {
	if err == nil {
		return &HostError{Code: "ssh_failed", Message: "ssh exited"}
	}
	if he, ok := err.(*HostError); ok {
		return he
	}
	return &HostError{Code: "ssh_failed", Message: err.Error()}
}
func terminal(e *HostError) bool {
	return e != nil && (e.Code == "ssh_auth_required" || e.Code == "ssh_host_key_unknown")
}
func remediate(cfg config.HostConfig, e *HostError) *HostError {
	if e == nil {
		return nil
	}
	args := append([]string{}, cfg.SSHArgs...)
	args = append(args, cfg.SSH)
	return &HostError{Code: e.Code, Message: e.Message + "\nrun: ssh " + strings.Join(args, " ")}
}
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}
func waitCtx(ctx context.Context, ch <-chan time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	}
}
