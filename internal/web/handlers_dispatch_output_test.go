package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestAPIDispatchOutputTailsWithoutCollection(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)
	streamDir := filepath.Join(dir, "state", "dispatches")
	if err := os.MkdirAll(streamDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := consult.Record{ID: "d-output", Kind: "dispatch", Harness: "unknown", Status: consult.StatusDone, StartedAt: time.Now()}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(streamDir, "d-output.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(streamDir, "d-output.ndjson"), []byte("{\"t\":0,\"raw\":\"one\\ntwo\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/dispatch/d-output/output?tail=1", nil)
	req.SetPathValue("id", "d-output")
	s.handleAPIDispatchOutput(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Data consult.Output `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data.Lines) != 1 || got.Data.Lines[0] != "                  two" || !got.Data.Truncated {
		t.Fatalf("output = %+v", got.Data)
	}
	if _, err := os.Stat(filepath.Join(streamDir, "d-output.json")); err != nil {
		t.Fatalf("output collected record: %v", err)
	}
}

func TestAPIDispatchOutputRejectsBadTail(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	for _, raw := range []string{"0", "-1", "wat"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/dispatch/d-x/output?tail="+raw, nil)
		req.SetPathValue("id", "d-x")
		s.handleAPIDispatchOutput(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("tail %q status = %d", raw, w.Code)
		}
	}
}
