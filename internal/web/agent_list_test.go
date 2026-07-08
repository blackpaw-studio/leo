package web

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAgentList_ConcurrentCallerGetsStaleCacheImmediately verifies that a
// caller which arrives while a refresh is already shelling out gets the
// previous cached value back right away, instead of blocking until the
// slow fetch completes. This is the behavior that regressed when
// fetchAgentList (a `claude agents` shell-out with up to a 10s timeout) ran
// while agentMu was held.
func TestAgentList_ConcurrentCallerGetsStaleCacheImmediately(t *testing.T) {
	s, _ := newTestServer(t)

	// Seed a stale cache so the next call is due for a refresh.
	s.agentMu.Lock()
	s.agentCache = []string{"old-agent"}
	s.agentsFetched = time.Now().Add(-2 * time.Minute)
	s.agentMu.Unlock()

	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	var fetchCalls int32

	s.fetchAgentListFn = func() []string {
		atomic.AddInt32(&fetchCalls, 1)
		close(fetchStarted)
		<-releaseFetch
		return []string{"new-agent"}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var leaderResult []string
	go func() {
		defer wg.Done()
		leaderResult = s.agentList()
	}()

	// Wait until the leader is inside the (slow, unlocked) fetch.
	select {
	case <-fetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fetch to start")
	}

	// A concurrent caller during the in-flight refresh must return promptly
	// with the stale cache, not block on the fetch.
	done := make(chan []string, 1)
	go func() {
		done <- s.agentList()
	}()

	select {
	case got := <-done:
		if len(got) != 1 || got[0] != "old-agent" {
			t.Fatalf("concurrent caller got %v, want stale cache [old-agent]", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("concurrent agentList() call blocked on in-flight fetch instead of returning stale cache")
	}

	// Only one shell-out should have been triggered by the two callers.
	if calls := atomic.LoadInt32(&fetchCalls); calls != 1 {
		t.Fatalf("fetchAgentListFn called %d times, want 1 (no duplicate concurrent shell-outs)", calls)
	}

	close(releaseFetch)
	wg.Wait()

	if len(leaderResult) != 1 || leaderResult[0] != "new-agent" {
		t.Fatalf("leader agentList() = %v, want [new-agent]", leaderResult)
	}

	// The cache should now reflect the completed refresh.
	s.agentMu.Lock()
	cache := s.agentCache
	s.agentMu.Unlock()
	if len(cache) != 1 || cache[0] != "new-agent" {
		t.Fatalf("s.agentCache = %v after refresh, want [new-agent]", cache)
	}
}

// TestAgentList_FreshCacheSkipsFetch verifies the fast path: a cache within
// the TTL is returned without invoking fetchAgentListFn at all.
func TestAgentList_FreshCacheSkipsFetch(t *testing.T) {
	s, _ := newTestServer(t)

	s.agentMu.Lock()
	s.agentCache = []string{"cached-agent"}
	s.agentsFetched = time.Now()
	s.agentMu.Unlock()

	s.fetchAgentListFn = func() []string {
		t.Fatal("fetchAgentListFn should not be called for a fresh cache")
		return nil
	}

	got := s.agentList()
	if len(got) != 1 || got[0] != "cached-agent" {
		t.Fatalf("agentList() = %v, want [cached-agent]", got)
	}
}
