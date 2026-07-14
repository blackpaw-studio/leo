package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/cron"
)

// TestServiceLogTail exercises GET /web/service/logtail: last-N-lines
// behavior, the missing-file friendly fallback, HTML escaping, and the
// server-side line cap.
func TestServiceLogTail(t *testing.T) {
	t.Run("returns last N lines", func(t *testing.T) {
		s, _ := newTestServer(t)

		logPath := s.serviceLogPath
		if err := os.MkdirAll(filepath.Dir(logPath), 0750); err != nil {
			t.Fatalf("mkdir state dir: %v", err)
		}
		if err := os.WriteFile(logPath, []byte("line1\nline2\nline3\n"), 0644); err != nil {
			t.Fatalf("writing log file: %v", err)
		}

		req := httptest.NewRequest("GET", "/web/service/logtail?n=2", nil)
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		body := readBody(t, w)
		if strings.Contains(body, "line1") {
			t.Errorf("expected body to NOT contain line1, got: %s", body)
		}
		if !strings.Contains(body, "line3") {
			t.Errorf("expected body to contain line3, got: %s", body)
		}
	})

	t.Run("missing file returns friendly message, not an error", func(t *testing.T) {
		s, _ := newTestServer(t)

		req := httptest.NewRequest("GET", "/web/service/logtail", nil)
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		body := readBody(t, w)
		if !strings.Contains(body, "no log file yet") {
			t.Errorf("expected friendly missing-file message, got: %s", body)
		}
	})

	t.Run("HTML-escapes log content", func(t *testing.T) {
		s, _ := newTestServer(t)

		logPath := s.serviceLogPath
		os.MkdirAll(filepath.Dir(logPath), 0750)
		payload := "before <script>alert(1)</script> after\n"
		if err := os.WriteFile(logPath, []byte(payload), 0644); err != nil {
			t.Fatalf("writing log file: %v", err)
		}

		req := httptest.NewRequest("GET", "/web/service/logtail", nil)
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)

		body := readBody(t, w)
		if strings.Contains(body, "<script>") {
			t.Errorf("raw <script> tag leaked into response, escaping failed: %s", body)
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in body, got: %s", body)
		}
	})

	t.Run("n is capped at 1000", func(t *testing.T) {
		s, _ := newTestServer(t)

		logPath := s.serviceLogPath
		os.MkdirAll(filepath.Dir(logPath), 0750)

		var sb strings.Builder
		for i := 0; i < 2000; i++ {
			sb.WriteString("logline\n")
		}
		if err := os.WriteFile(logPath, []byte(sb.String()), 0644); err != nil {
			t.Fatalf("writing log file: %v", err)
		}

		req := httptest.NewRequest("GET", "/web/service/logtail?n=5000", nil)
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)

		body := readBody(t, w)
		lines := strings.Count(body, "logline")
		if lines > 1000 {
			t.Errorf("expected line count capped at 1000, got %d", lines)
		}
	})
}

// TestPageServiceShowsProcessTableAndRestartWarning verifies the /service
// page renders the supervisor table (with an "(agent)" marker for ephemeral
// entries) and that the Restart button's hx-confirm text names both
// processes and agents as being restarted.
func TestPageServiceShowsProcessTableAndRestartWarning(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-web-service-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	writeTestConfig(t, dir)
	cfgPath := filepath.Join(dir, "leo.yaml")

	processes := &mockProcesses{
		states: map[string]ProcessStateInfo{
			"assistant": {
				Name:      "assistant",
				Status:    "running",
				StartedAt: time.Now().Add(-2 * time.Hour),
				Restarts:  0,
			},
			"my-agent-1": {
				Name:      "my-agent-1",
				Status:    "running",
				StartedAt: time.Now().Add(-10 * time.Minute),
				Ephemeral: true,
			},
		},
	}
	scheduler := &mockScheduler{
		entries: []cron.EntryInfo{
			{Name: "heartbeat", Schedule: "0 * * * *", Next: time.Now().Add(30 * time.Minute)},
		},
	}
	reloader := &mockReloader{}

	s := New(cfgPath, processes, scheduler, reloader, nil, Options{Port: testPort, APIToken: testAPIToken})
	rawHandler := s.httpServer.Handler
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizeTestRequest(r)
		rawHandler.ServeHTTP(w, r)
	})
	s.httpServer.Handler = wrapped

	req := httptest.NewRequest("GET", "/service", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := readBody(t, w)

	if !strings.Contains(body, "assistant") {
		t.Error("expected process table to contain 'assistant'")
	}
	if !strings.Contains(body, "my-agent-1") {
		t.Error("expected process table to contain 'my-agent-1'")
	}
	if !strings.Contains(body, "(agent)") {
		t.Error("expected ephemeral row to be marked with '(agent)'")
	}
	if !strings.Contains(body, `hx-post="/web/service/restart"`) {
		t.Error("expected Restart service button wired to /web/service/restart")
	}
	if !strings.Contains(body, `hx-post="/web/config/reload"`) {
		t.Error("expected Reload config button wired to /web/config/reload")
	}

	// hx-confirm text must name agents restarting (agents-only copy; sessions
	// were collapsed into agents).
	confirmIdx := strings.Index(body, "hx-confirm=")
	if confirmIdx == -1 {
		t.Fatal("expected an hx-confirm attribute on the Restart service button")
	}
	confirmSnippet := body[confirmIdx : confirmIdx+250]
	if !strings.Contains(strings.ToLower(confirmSnippet), "agent") {
		t.Errorf("hx-confirm text should mention agents: %s", confirmSnippet)
	}
}
