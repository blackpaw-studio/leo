package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// seedAgentCache sets the cache and its fetch stamp under the lock.
func seedAgentCache(s *Server, names []string, fetched time.Time) {
	s.agentMu.Lock()
	s.agentCache = names
	s.agentsFetched = fetched
	s.agentMu.Unlock()
}

func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A cold cache must not make GET /config/schema wait for `claude agents`.
func TestConfigSchema_ColdAgentCacheDoesNotBlock(t *testing.T) {
	s, _ := newTestServer(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{}, 4)
	s.fetchAgentListFn = func() []string {
		started <- struct{}{}
		<-release
		return []string{"slow-agent"}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		s.handleConfigSchema(rec, httptest.NewRequest(http.MethodGet, "/config/schema", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d", rec.Code)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("schema request blocked on the agent probe")
	}
}

// Once the background refresh lands, the next call sees the names.
func TestAgentList_BackgroundFetchPopulatesNextCall(t *testing.T) {
	s, _ := newTestServer(t)
	done := make(chan struct{})
	s.fetchAgentListFn = func() []string {
		defer close(done)
		return []string{"alpha", "beta"}
	}

	if got := s.agentList(); len(got) != 0 {
		t.Fatalf("cold agentList() = %v, want empty", got)
	}
	awaitClosed(t, done, "background fetch")
	// The fetch goroutine stores the result after fn returns; poll the lock.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := s.agentList(); len(got) == 2 && got[0] == "alpha" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cache never populated")
		}
		time.Sleep(time.Millisecond)
	}
	body := httptest.NewRecorder()
	s.handleConfigSchema(body, httptest.NewRequest(http.MethodGet, "/config/schema", nil))
	if !strings.Contains(body.Body.String(), "alpha") {
		t.Fatalf("schema lacks agent names: %s", body.Body.String())
	}
}

// Concurrent stale callers get the old cache and share one refresh.
func TestAgentList_StaleReturnsOldCacheSingleFlight(t *testing.T) {
	s, _ := newTestServer(t)
	seedAgentCache(s, []string{"old-agent"}, time.Now().Add(-2*time.Minute))

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls int32
	s.fetchAgentListFn = func() []string {
		atomic.AddInt32(&calls, 1)
		started <- struct{}{}
		<-release
		return []string{"new-agent"}
	}

	for i := 0; i < 5; i++ {
		got := s.agentList()
		if len(got) != 1 || got[0] != "old-agent" {
			t.Fatalf("call %d = %v, want [old-agent]", i, got)
		}
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fetch start")
	}

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("fetch calls = %d, want 1", n)
	}
}

func TestAgentList_FreshCacheSkipsFetch(t *testing.T) {
	s, _ := newTestServer(t)
	seedAgentCache(s, []string{"cached-agent"}, time.Now())
	var fetches atomic.Int32
	s.fetchAgentListFn = func() []string {
		fetches.Add(1)
		return nil
	}
	if got := s.agentList(); len(got) != 1 || got[0] != "cached-agent" {
		t.Fatalf("agentList() = %v", got)
	}
	if n := fetches.Load(); n != 0 {
		t.Errorf("fetch ran %d times for a fresh cache, want 0", n)
	}
}

// A failed refresh keeps the old cache and still advances the TTL clock.
func TestAgentList_FailedRefreshKeepsOldCache(t *testing.T) {
	s, _ := newTestServer(t)
	seedAgentCache(s, []string{"good-agent"}, time.Now().Add(-2*time.Minute))
	done := make(chan struct{})
	s.fetchAgentListFn = func() []string { defer close(done); return nil }

	if got := s.agentList(); len(got) != 1 || got[0] != "good-agent" {
		t.Fatalf("agentList() = %v", got)
	}
	awaitClosed(t, done, "fetch")
	s.agentMu.Lock()
	cache, fetched := s.agentCache, s.agentsFetched
	s.agentMu.Unlock()
	if len(cache) != 1 || cache[0] != "good-agent" {
		t.Fatalf("cache = %v, want preserved", cache)
	}
	if time.Since(fetched) > time.Second {
		t.Error("TTL clock should advance even when the fetch fails")
	}
}

// WarmAgentList primes the cache without a request.
func TestWarmAgentList_PopulatesCache(t *testing.T) {
	s, _ := newTestServer(t)
	done := make(chan struct{})
	s.fetchAgentListFn = func() []string { defer close(done); return []string{"warm"} }
	s.WarmAgentList()
	awaitClosed(t, done, "warm fetch")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := s.agentList(); len(got) == 1 && got[0] == "warm" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("cache never warmed")
		}
		time.Sleep(time.Millisecond)
	}
}
