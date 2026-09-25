package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/observe"
)

var surfaceStartedAt = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// sequencedProcesses returns states from next, letting a test change an
// agent's incarnation between the handler's first read and its re-check.
type sequencedProcesses struct {
	mu    sync.Mutex
	calls int
	next  func(call int) map[string]ProcessStateInfo
}

func (p *sequencedProcesses) States() map[string]ProcessStateInfo {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	return p.next(call)
}

func liveAgentStates(startedAt time.Time) map[string]ProcessStateInfo {
	return map[string]ProcessStateInfo{
		"alpha":     {Name: "alpha", Status: "running", StartedAt: startedAt, Ephemeral: true},
		"beta":      {Name: "beta", Status: "running", StartedAt: startedAt, Ephemeral: true},
		"stopped":   {Name: "stopped", Status: "stopped", StartedAt: startedAt, Ephemeral: true},
		"assistant": {Name: "assistant", Status: "running", StartedAt: startedAt},
	}
}

// newSurfaceServer wires two live agents (alpha, beta) whose workspaces hold
// real files, plus a stopped agent and a non-ephemeral process.
func newSurfaceServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, _, svc := newTestServerWithAgents(t)
	ws := t.TempDir()
	for _, rel := range []string{"main.go", "dir with space/ü file.md", "sub/x.txt"} {
		p := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	svc.records = []agent.Record{
		{Name: "alpha", Workspace: ws},
		{Name: "beta", Workspace: ws},
		{Name: "stopped", Workspace: ws},
	}
	s.processes = &mockProcesses{states: liveAgentStates(surfaceStartedAt)}
	s.surfacedFiles = observe.NewSurfacedFileStore(nil, nil)
	return s, ws
}

func postSurface(t *testing.T, s *Server, agentName, body string) (int, apiResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/"+agentName+"/surface-file", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return w.Code, resp
}

func surfaceBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSurfaceFileRecordsRelativeAndAbsolutePaths(t *testing.T) {
	s, ws := newSurfaceServer(t)
	for _, tc := range []struct {
		name, path, wantAbs string
	}{
		{"relative", "main.go", filepath.Join(ws, "main.go")},
		{"relative with dot segments", "./sub/../main.go", filepath.Join(ws, "main.go")},
		{"spaces and unicode", "dir with space/ü file.md", filepath.Join(ws, "dir with space/ü file.md")},
		{"absolute", filepath.Join(ws, "sub", "x.txt"), filepath.Join(ws, "sub", "x.txt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := postSurface(t, s, "alpha", surfaceBody(t, map[string]any{"path": tc.path}))
			if code != http.StatusOK || !resp.OK {
				t.Fatalf("status %d, resp %+v", code, resp)
			}
			var data struct{ ID string }
			raw, _ := json.Marshal(resp.Data)
			_ = json.Unmarshal(raw, &data)
			files := s.surfacedFiles.Get("alpha")
			got := files[len(files)-1]
			if data.ID == "" || got.ID != data.ID {
				t.Fatalf("returned id %q, stored %q", data.ID, got.ID)
			}
			if got.Path != tc.path || got.AbsPath != tc.wantAbs || !got.StartedAt.Equal(surfaceStartedAt) {
				t.Fatalf("stored %+v; want path %q abs %q", got, tc.path, tc.wantAbs)
			}
		})
	}
}

func TestSurfaceFileLineAndReason(t *testing.T) {
	s, _ := newSurfaceServer(t)
	reason200 := strings.Repeat("é", observe.MaxSurfaceReasonRunes)

	code, resp := postSurface(t, s, "alpha", surfaceBody(t, map[string]any{"path": "main.go", "line": 1, "reason": reason200}))
	if code != http.StatusOK {
		t.Fatalf("200-rune reason: status %d, %+v", code, resp)
	}
	got := s.surfacedFiles.Get("alpha")[0]
	if got.Line != 1 || got.Reason != reason200 {
		t.Fatalf("stored %+v", got)
	}
}

func TestSurfaceFileRejections(t *testing.T) {
	for _, tc := range []struct {
		name, agent, body string
		want              int
	}{
		{"empty path", "alpha", `{"path":""}`, http.StatusBadRequest},
		{"missing path", "alpha", `{}`, http.StatusBadRequest},
		{"missing file", "alpha", `{"path":"nope.go"}`, http.StatusBadRequest},
		{"directory", "alpha", `{"path":"sub"}`, http.StatusBadRequest},
		{"line zero", "alpha", `{"path":"main.go","line":0}`, http.StatusBadRequest},
		{"line negative", "alpha", `{"path":"main.go","line":-3}`, http.StatusBadRequest},
		{"line fractional", "alpha", `{"path":"main.go","line":1.5}`, http.StatusBadRequest},
		{"reason 201 runes", "alpha", `{"path":"main.go","reason":"` + strings.Repeat("é", observe.MaxSurfaceReasonRunes+1) + `"}`, http.StatusBadRequest},
		{"malformed json", "alpha", `{"path":`, http.StatusBadRequest},
		{"dispatch subagent", "alpha", `{"path":"main.go","dispatch_id":"d-123"}`, http.StatusForbidden},
		{"unknown agent", "ghost", `{"path":"main.go"}`, http.StatusForbidden},
		{"stopped agent", "stopped", `{"path":"main.go"}`, http.StatusForbidden},
		{"non-ephemeral process", "assistant", `{"path":"main.go"}`, http.StatusForbidden},
		{"task process", "task:heartbeat", `{"path":"main.go"}`, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newSurfaceServer(t)

			code, resp := postSurface(t, s, tc.agent, tc.body)

			if code != tc.want || resp.OK || resp.Error == "" {
				t.Fatalf("status %d resp %+v; want %d with an error", code, resp, tc.want)
			}
			if all := s.surfacedFiles.All(); len(all) != 0 {
				t.Fatalf("rejected call stored %+v", all)
			}
		})
	}
}

func TestSurfaceFileDispatchRejectionIsExplicit(t *testing.T) {
	s, _ := newSurfaceServer(t)
	_, resp := postSurface(t, s, "alpha", `{"path":"main.go","dispatch_id":"d-1"}`)
	if !strings.Contains(resp.Error, "dispatch") {
		t.Fatalf("error %q does not explain the dispatch rejection", resp.Error)
	}
}

func TestSurfaceFileRejectsIncarnationChangeDuringStat(t *testing.T) {
	s, _ := newSurfaceServer(t)
	restarted := surfaceStartedAt.Add(time.Second)
	s.processes = &sequencedProcesses{next: func(call int) map[string]ProcessStateInfo {
		if call == 1 {
			return liveAgentStates(surfaceStartedAt)
		}
		return liveAgentStates(restarted)
	}}

	code, resp := postSurface(t, s, "alpha", `{"path":"main.go"}`)

	if code != http.StatusConflict || resp.OK {
		t.Fatalf("status %d resp %+v; want 409", code, resp)
	}
	if got := s.surfacedFiles.Get("alpha"); len(got) != 0 {
		t.Fatalf("stale submission stored %+v", got)
	}
}

func TestSurfaceFileRejectsResetLandingAfterRecheck(t *testing.T) {
	s, _ := newSurfaceServer(t)
	store := s.surfacedFiles
	s.processes = &sequencedProcesses{next: func(call int) map[string]ProcessStateInfo {
		if call == 2 {
			// The supervisor moves the store to the next incarnation after
			// the handler's re-check read the old one.
			store.Reset("alpha", surfaceStartedAt.Add(time.Second))
		}
		return liveAgentStates(surfaceStartedAt)
	}}

	code, _ := postSurface(t, s, "alpha", `{"path":"main.go"}`)

	if code != http.StatusConflict {
		t.Fatalf("status %d, want 409", code)
	}
	if got := store.Get("alpha"); len(got) != 0 {
		t.Fatalf("stale submission stored %+v", got)
	}
}

func TestSurfaceFileAgentIsolation(t *testing.T) {
	s, _ := newSurfaceServer(t)
	postSurface(t, s, "alpha", `{"path":"main.go"}`)
	postSurface(t, s, "beta", `{"path":"sub/x.txt"}`)

	a, b := s.surfacedFiles.Get("alpha"), s.surfacedFiles.Get("beta")
	if len(a) != 1 || a[0].Path != "main.go" || a[0].Agent != "alpha" || len(b) != 1 || b[0].Path != "sub/x.txt" || b[0].Agent != "beta" {
		t.Fatalf("alpha %+v beta %+v", a, b)
	}
}

func TestSurfaceFileConcurrentCalls(t *testing.T) {
	s, _ := newSurfaceServer(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "alpha"
			if i%2 == 1 {
				name = "beta"
			}
			if code, resp := postSurface(t, s, name, `{"path":"main.go"}`); code != http.StatusOK {
				t.Errorf("status %d resp %+v", code, resp)
			}
		}(i)
	}
	wg.Wait()
	for _, name := range []string{"alpha", "beta"} {
		if got := len(s.surfacedFiles.Get(name)); got != observe.MaxSurfacedFiles {
			t.Fatalf("%s holds %d, want %d", name, got, observe.MaxSurfacedFiles)
		}
	}
}

func TestSurfaceFileUnavailableWithoutStore(t *testing.T) {
	s, _ := newSurfaceServer(t)
	s.surfacedFiles = nil
	code, _ := postSurface(t, s, "alpha", `{"path":"main.go"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", code)
	}
}

func TestSurfaceFileBodyLimits(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"oversized body", `{"path":"main.go","reason":"` + strings.Repeat("a", maxSurfaceFileBody) + `"}`, http.StatusRequestEntityTooLarge},
		{"oversized trailing data", `{"path":"main.go"}` + strings.Repeat(" ", maxSurfaceFileBody), http.StatusRequestEntityTooLarge},
		{"trailing garbage", `{"path":"main.go"} garbage`, http.StatusBadRequest},
		{"second json value", `{"path":"main.go"}{"path":"main.go"}`, http.StatusBadRequest},
		{"explicit null line", `{"path":"main.go","line":null}`, http.StatusBadRequest},
		{"explicit null reason", `{"path":"main.go","reason":null}`, http.StatusBadRequest},
		{"explicit null both", `{"path":"main.go","line":null,"reason":null}`, http.StatusBadRequest},
		{"null line other case", `{"path":"main.go","Line":null}`, http.StatusBadRequest},
		{"null reason other case", `{"path":"main.go","REASON":null}`, http.StatusBadRequest},
		{"duplicate line null first", `{"path":"main.go","line":null,"line":3}`, http.StatusBadRequest},
		{"duplicate line null last", `{"path":"main.go","line":3,"line":null}`, http.StatusBadRequest},
		{"duplicate key across case", `{"path":"main.go","line":2,"LINE":3}`, http.StatusBadRequest},
		{"duplicate path", `{"path":"nope.go","path":"main.go"}`, http.StatusBadRequest},
		{"duplicate path across case", `{"path":"a","PATH":"b"}`, http.StatusBadRequest},
		{"duplicate line mixed case", `{"path":"main.go","line":1,"LiNe":2}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newSurfaceServer(t)

			code, resp := postSurface(t, s, "alpha", tc.body)

			if code != tc.want || resp.OK || resp.Error == "" {
				t.Fatalf("status %d resp %+v; want %d with an error", code, resp, tc.want)
			}
			if all := s.surfacedFiles.All(); len(all) != 0 {
				t.Fatalf("rejected call stored %+v", all)
			}
		})
	}
}

func TestSurfaceFileTrailingWhitespaceAccepted(t *testing.T) {
	s, _ := newSurfaceServer(t)
	if code, resp := postSurface(t, s, "alpha", "{\"path\":\"main.go\"}\n"); code != http.StatusOK {
		t.Fatalf("status %d resp %+v", code, resp)
	}
}

func TestSurfaceFileCaseVariantKeyAccepted(t *testing.T) {
	s, _ := newSurfaceServer(t)
	if code, resp := postSurface(t, s, "alpha", `{"Path":"main.go","Line":4}`); code != http.StatusOK {
		t.Fatalf("status %d resp %+v", code, resp)
	}
	if got := s.surfacedFiles.Get("alpha"); len(got) != 1 || got[0].Line != 4 {
		t.Fatalf("stored %+v", got)
	}
}

func TestSurfaceFileIgnoresUnknownKeys(t *testing.T) {
	s, _ := newSurfaceServer(t)
	for _, body := range []string{
		`{"path":"main.go","Σ":1,"ς":2}`,
		`{"path":"main.go","foo":1,"FOO":2}`,
		`{"path":"main.go","foo":1,"foo":2}`,
	} {
		if code, resp := postSurface(t, s, "alpha", body); code != http.StatusOK {
			t.Fatalf("%s: status %d resp %+v; unknown keys must be ignored", body, code, resp)
		}
	}
}
