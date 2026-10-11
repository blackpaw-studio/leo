package web

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// revocationCheckInterval is how often an open device stream re-checks its
// token. Revocation usually comes from `leo devices revoke` in another
// process, so the daemon learns of it by re-reading the store; the check is
// a stat plus a hash compare.
const revocationCheckInterval = 2 * time.Second

// watchDeviceRevocation returns a channel closed once the device token r
// carries stops authenticating (revoked), and a stop func that releases the
// watcher. For a request not authenticated by a device token it returns a
// nil channel, which never fires, and a no-op stop.
func (s *Server) watchDeviceRevocation(r *http.Request) (<-chan struct{}, func()) {
	tok := extractBearer(r.Header.Get("Authorization"))
	if !s.isDeviceToken(tok) {
		return nil, func() {}
	}
	revoked := make(chan struct{})
	quit := make(chan struct{})
	ticks, stopTicks := s.revocationTicker(revocationCheckInterval)
	go func() {
		defer stopTicks()
		for {
			select {
			case <-quit:
				return
			case <-ticks:
				if !s.isDeviceToken(tok) {
					close(revoked)
					return
				}
			}
		}
	}()
	var once sync.Once
	return revoked, func() { once.Do(func() { close(quit) }) }
}

// contextEndingOnRevoke derives r's context so that it is canceled when the
// device token r carries is revoked. The returned func releases it.
func (s *Server) contextEndingOnRevoke(r *http.Request) (context.Context, func()) {
	revoked, stopWatch := s.watchDeviceRevocation(r)
	ctx, cancel := context.WithCancel(r.Context())
	if revoked != nil {
		go func() {
			select {
			case <-revoked:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, func() { stopWatch(); cancel() }
}
