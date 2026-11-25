// Package peercred reports which process is on the other end of a unix
// socket connection, as the kernel records it. A session-binding decision
// needs a caller identity the caller cannot choose: a pid written in a
// request body is whatever its sender says, while the kernel's peer pid is
// the process that connected.
package peercred

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
)

// errNotUnix is a connection that has no kernel-recorded peer process.
var errNotUnix = errors.New("peercred: not a unix socket connection")

// Peer is what a unix connection's kernel record said about its far end.
type Peer struct {
	PID int
	Err error // why PID could not be read; PID is then 0
}

type ctxKey struct{}

// PID is the process id of conn's peer, from the kernel (LOCAL_PEERPID on
// darwin, SO_PEERCRED on linux). Any connection that is not a unix socket, and
// any platform without a lookup, is an error: callers fail closed.
func PID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errNotUnix
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peercred: %w", err)
	}
	var pid int
	var lookupErr error
	if err := raw.Control(func(fd uintptr) {
		if fd > math.MaxInt32 {
			lookupErr = fmt.Errorf("descriptor %d out of range", fd)
			return
		}
		pid, lookupErr = peerPID(int(fd))
	}); err != nil {
		return 0, fmt.Errorf("peercred: %w", err)
	}
	if lookupErr != nil {
		return 0, fmt.Errorf("peercred: %w", lookupErr)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("peercred: kernel reported pid %d", pid)
	}
	return pid, nil
}

// ConnContext is an http.Server.ConnContext: it records the peer of a unix
// connection in the context every request on it carries. A connection of any
// other kind records nothing.
func ConnContext(ctx context.Context, conn net.Conn) context.Context {
	if _, ok := conn.(*net.UnixConn); !ok {
		return ctx
	}
	pid, err := PID(conn)
	return WithPeer(ctx, Peer{PID: pid, Err: err})
}

// WithPeer returns ctx carrying peer.
func WithPeer(ctx context.Context, peer Peer) context.Context {
	return context.WithValue(ctx, ctxKey{}, peer)
}

// FromContext returns the peer ConnContext (or WithPeer) recorded.
func FromContext(ctx context.Context) (Peer, bool) {
	peer, ok := ctx.Value(ctxKey{}).(Peer)
	return peer, ok
}

// VerifiedPID is the kernel-verified pid of the process that sent the request
// ctx belongs to, and false when there is none: the request came over TCP, or
// the lookup failed. It is the only caller identity to base a trust decision
// on.
func VerifiedPID(ctx context.Context) (int, bool) {
	peer, ok := FromContext(ctx)
	if !ok || peer.Err != nil || peer.PID <= 0 {
		return 0, false
	}
	return peer.PID, true
}
