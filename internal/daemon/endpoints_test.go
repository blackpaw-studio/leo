package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newServerWithRouter builds a Server that has only the session router wired
// (no other dependencies like ProcessStateProvider). Tests bind the three
// new endpoints to httptest.
func newServerWithRouter(t *testing.T) (*Server, *sessionRouter, *httptest.Server) {
	t.Helper()
	s := &Server{router: newSessionRouter()}
	s.router.SetInjector(func(session, prompt string) error { return nil })
	s.router.SetAborter(func(session string) error { return nil })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /task/enqueue", s.handleTaskEnqueue)
	mux.HandleFunc("GET /task/await", s.handleTaskAwait)
	mux.HandleFunc("POST /task/report", s.handleTaskReport)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, s.router, ts
}

func TestEnqueueRouteAccepts(t *testing.T) {
	_, _, ts := newServerWithRouter(t)
	body, _ := json.Marshal(map[string]any{
		"session":         "leo-session-foo",
		"task":            "t",
		"prompt":          "do it",
		"channels":        []string{"plugin:slack@official"},
		"queue_max":       3,
		"timeout_seconds": 10,
	})
	resp, err := http.Post(ts.URL+"/task/enqueue", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["accepted"] != true || out["invocation_id"] == "" {
		t.Fatalf("body: %#v", out)
	}
}

func TestAwaitGetsReport(t *testing.T) {
	_, _, ts := newServerWithRouter(t)
	enqBody, _ := json.Marshal(map[string]any{
		"session":         "leo-session-foo",
		"task":            "t",
		"prompt":          "x",
		"queue_max":       3,
		"timeout_seconds": 5,
	})
	enq, _ := http.Post(ts.URL+"/task/enqueue", "application/json", bytes.NewReader(enqBody))
	var enqOut map[string]any
	_ = json.NewDecoder(enq.Body).Decode(&enqOut)
	enq.Body.Close()
	id := enqOut["invocation_id"].(string)

	go func() {
		time.Sleep(30 * time.Millisecond)
		rep, _ := json.Marshal(map[string]any{
			"invocation_id": id,
			"session_id":    "csid-1",
			"final_message": "result!",
		})
		repResp, err := http.Post(ts.URL+"/task/report", "application/json", bytes.NewReader(rep))
		if err != nil {
			return
		}
		repResp.Body.Close()
	}()

	aw, err := http.Get(ts.URL + "/task/await?invocation_id=" + id)
	if err != nil {
		t.Fatalf("await get: %v", err)
	}
	defer aw.Body.Close()
	if aw.StatusCode != http.StatusOK {
		t.Fatalf("await status: %d", aw.StatusCode)
	}
	var awOut map[string]any
	_ = json.NewDecoder(aw.Body).Decode(&awOut)
	if awOut["ok"] != true || awOut["final_message"] != "result!" || awOut["session_id"] != "csid-1" {
		t.Fatalf("await body: %#v", awOut)
	}
}

func TestEnqueueRejectsOnQueueFull(t *testing.T) {
	_, _, ts := newServerWithRouter(t)
	body, _ := json.Marshal(map[string]any{
		"session":         "leo-session-foo-full",
		"task":            "t",
		"prompt":          "x",
		"queue_max":       1,
		"timeout_seconds": 5,
	})
	var lastStatus int
	var lastBody map[string]any
	for i := 0; i < 2; i++ {
		resp, err := http.Post(ts.URL+"/task/enqueue", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		lastStatus = resp.StatusCode
		_ = json.NewDecoder(resp.Body).Decode(&lastBody)
		resp.Body.Close()
	}
	if lastStatus != http.StatusOK {
		t.Fatalf("status: %d", lastStatus)
	}
	if lastBody["accepted"] != false || !strings.Contains(lastBody["reason"].(string), "queue full") {
		t.Fatalf("expected queue full rejection, got %#v", lastBody)
	}
}
