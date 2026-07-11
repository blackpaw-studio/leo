//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/session"
)

// persistentMarkerRe matches the invocation marker that the persistent
// runner bakes into every prompt. We mirror the production regex so tests
// don't depend on internal package symbols.
var persistentMarkerRe = regexp.MustCompile(`<!-- leo:invocation=([0-9a-f]{32}) -->`)

// startDaemon boots a daemon.Server bound to <ws>/state/leo.sock with the
// given config path and a no-op process state provider. It registers a
// cleanup that shuts the server down. Returns the live server.
func startDaemon(t *testing.T, ws, cfgPath string) *daemon.Server {
	t.Helper()

	stateDir := filepath.Join(ws, "state")
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}
	sockPath := filepath.Join(stateDir, "leo.sock")

	srv := daemon.New(sockPath, cfgPath, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("starting daemon: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	deadline := time.Now().Add(2 * time.Second)
	for !daemon.IsRunning(ws) {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become ready within 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv
}

// promptCapture records every (session, prompt) pair sent to the injector.
// It's safe for concurrent calls because the daemon pump may invoke the
// injector from a goroutine.
type promptCapture struct {
	mu   sync.Mutex
	rows []capturedPrompt
}

type capturedPrompt struct {
	Session string
	Prompt  string
	InvID   string
}

func (c *promptCapture) record(session, prompt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, capturedPrompt{
		Session: session,
		Prompt:  prompt,
		InvID:   extractMarker(prompt),
	})
}

func (c *promptCapture) snapshot() []capturedPrompt {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedPrompt, len(c.rows))
	copy(out, c.rows)
	return out
}

func (c *promptCapture) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rows)
}

// extractMarker pulls the invocation id out of the prompt, returning "" if
// the marker is missing.
func extractMarker(prompt string) string {
	m := persistentMarkerRe.FindStringSubmatch(prompt)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// installAutoResponder wires the daemon's injector to (1) record every
// prompt and (2) fire-and-forget a Stop-hook style report back via the
// public Unix socket. The simulated "claude session" id is derived from
// the session name so callers can assert resume continuity.
func installAutoResponder(t *testing.T, srv *daemon.Server, ws string, cap *promptCapture) {
	t.Helper()
	srv.SetInjector(func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		cap.record(session, prompt)
		invID := extractMarker(prompt)
		if invID == "" {
			// No marker — this is something we don't recognise; refuse so
			// the test surface fails loudly rather than hanging.
			return nil, fmt.Errorf("auto-responder: missing invocation marker in prompt")
		}
		sessionID := "csid-" + session
		finalMsg := "FAKE-REPLY: " + truncate80(strings.TrimSpace(stripMarker(prompt)))
		go func(invID, sessionID, finalMsg, sessionName string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := daemon.ReportTask(ctx, ws, invID, sessionID, finalMsg, sessionName); err != nil {
				t.Logf("auto-responder: report invID=%s err=%v", invID, err)
			}
		}(invID, sessionID, finalMsg, session)
		return nil, nil
	})
	srv.SetAborter(func(session string) error { return nil })
}

// installGatedResponder behaves like installAutoResponder but only fires
// each report after the corresponding gate is closed (release(invID)).
// Useful for queue-depth assertions.
type gatedResponder struct {
	mu      sync.Mutex
	pending map[string]chan struct{}
	cap     *promptCapture
}

func newGatedResponder(cap *promptCapture) *gatedResponder {
	return &gatedResponder{
		pending: make(map[string]chan struct{}),
		cap:     cap,
	}
}

// gate returns the release channel for the given invocation id, creating
// it on first reference.
func (g *gatedResponder) gate(invID string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch, ok := g.pending[invID]; ok {
		return ch
	}
	ch := make(chan struct{})
	g.pending[invID] = ch
	return ch
}

// release closes the gate for invID, letting the responder report.
func (g *gatedResponder) release(invID string) {
	g.mu.Lock()
	ch, ok := g.pending[invID]
	if !ok {
		ch = make(chan struct{})
		g.pending[invID] = ch
	}
	g.mu.Unlock()
	select {
	case <-ch:
		// already closed
	default:
		close(ch)
	}
}

func installGatedResponder(t *testing.T, srv *daemon.Server, ws string, g *gatedResponder) {
	t.Helper()
	srv.SetInjector(func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		g.cap.record(session, prompt)
		invID := extractMarker(prompt)
		if invID == "" {
			return nil, fmt.Errorf("gated responder: missing invocation marker")
		}
		ch := g.gate(invID)
		go func(invID, session string) {
			<-ch
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sessionID := "csid-" + session
			finalMsg := "FAKE-REPLY"
			if err := daemon.ReportTask(ctx, ws, invID, sessionID, finalMsg, session); err != nil {
				t.Logf("gated responder: report invID=%s err=%v", invID, err)
			}
		}(invID, session)
		return nil, nil
	})
	srv.SetAborter(func(session string) error { return nil })
}

// pollHistoryEntry waits up to timeout for a history entry to appear for
// the given task with the given exit code. It returns the matched entry.
func pollHistoryEntry(t *testing.T, ws, taskName string, wantExit int, timeout time.Duration) history.Entry {
	t.Helper()
	store := history.NewStore(ws)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries := store.GetAll(taskName)
		for _, e := range entries {
			if e.ExitCode == wantExit {
				return e
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("history entry for task %q with exit %d did not appear within %s", taskName, wantExit, timeout)
	return history.Entry{}
}

// readStoredSessionID returns the persisted claude session id for the
// given session name (the key under which the persistent runner stores
// it), or "" if not yet persisted.
func readStoredSessionID(t *testing.T, ws, sessionName string) string {
	t.Helper()
	store := session.NewStore(ws)
	id, found, err := store.Get("session:" + sessionName)
	if err != nil {
		t.Fatalf("reading session store: %v", err)
	}
	if !found {
		return ""
	}
	return id
}

// pollStoredSessionID waits up to timeout for the session id to be
// persisted, returning the final value.
func pollStoredSessionID(t *testing.T, ws, sessionName string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if id := readStoredSessionID(t, ws, sessionName); id != "" {
			return id
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session id for %q was not persisted within %s", sessionName, timeout)
	return ""
}

// stripMarker removes the leading "<!-- leo:invocation=... -->\n" line so
// the FAKE-REPLY echo shows the task content rather than the marker.
func stripMarker(prompt string) string {
	if idx := strings.Index(prompt, "-->\n"); idx >= 0 {
		return prompt[idx+len("-->\n"):]
	}
	return prompt
}

// truncate80 mirrors the fakeclaude FAKE-REPLY echo length to keep
// assertions deterministic.
func truncate80(s string) string {
	if len(s) <= 80 {
		return s
	}
	return s[:80]
}
