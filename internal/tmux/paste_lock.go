package tmux

import (
	"context"
	"sync"
)

// sessionPasteLocks serializes the pastes into each tmux session. Every
// paste into a session stages its text in the session's one buffer (see
// sessionBufferName), and its readiness probe ends with a Ctrl-U that would
// clear another paste's text from the composer, so two at once can deliver
// one prompt twice, or none. A session's lock is held from the probe to the
// submitting Enter, and kept only while some paste holds or awaits it.
type sessionPasteLocks struct {
	mu    sync.Mutex
	locks map[string]*sessionPasteLock
}

type sessionPasteLock struct {
	held chan struct{} // a one-slot semaphore
	refs int           // pastes holding or awaiting it
}

// pasteLocks is package state on purpose: every paste into a session goes
// through injectPromptProfile, so no caller can bypass the lock.
var pasteLocks = &sessionPasteLocks{locks: map[string]*sessionPasteLock{}}

// acquire waits for session's lock, or for ctx to end. The lock is keyed by
// session name alone: tmux paths that differ only in spelling reach the
// same server, and must not split it.
func (l *sessionPasteLocks) acquire(ctx context.Context, session string) (release func(), err error) {
	l.mu.Lock()
	lk := l.locks[session]
	if lk == nil {
		lk = &sessionPasteLock{held: make(chan struct{}, 1)}
		l.locks[session] = lk
	}
	lk.refs++
	l.mu.Unlock()

	select {
	case lk.held <- struct{}{}:
		return func() {
			<-lk.held
			l.unref(session, lk)
		}, nil
	case <-ctx.Done():
		l.unref(session, lk)
		return nil, ctx.Err()
	}
}

func (l *sessionPasteLocks) unref(session string, lk *sessionPasteLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lk.refs--
	if lk.refs == 0 {
		delete(l.locks, session)
	}
}

// LockSessionInput holds session's input for a caller that writes into its
// composer by other means than InjectPrompt (which holds it itself), until
// unlock; it waits for any paste under way, or for ctx to end.
func LockSessionInput(ctx context.Context, session string) (unlock func(), err error) {
	return pasteLocks.acquire(ctx, session)
}

// size is how many sessions have a paste holding or awaiting their lock.
func (l *sessionPasteLocks) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}
