package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

type fakeDispatchBridge map[string]bool

func (f fakeDispatchBridge) BridgeOwnsReports(id string) bool { return f[id] }

// Once a dispatch's bridge drives its turn state, the claude shell hooks
// reporting the same moments are acknowledged (so they stop retrying) but
// not applied: applying both would double every turn.
func TestDispatchReportHTTPLeavesBridgedDispatchesToTheBridge(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.dispatchBridge = fakeDispatchBridge{"d-br": true}
	for _, tc := range []struct {
		id          string
		wantIgnored bool
	}{{"d-br", true}, {"d-hooks", false}} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/dispatch/"+tc.id+"/report", strings.NewReader(`{"event_id":"e","payload":{"hook_event_name":"Stop"}}`))
		req.SetPathValue("id", tc.id)
		s.handleAPIDispatchReport(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.id, w.Code, w.Body.String())
		}
		body, _ := io.ReadAll(w.Body)
		if got := strings.Contains(string(body), "superseded_by_bridge"); got != tc.wantIgnored {
			t.Fatalf("%s: ignored=%v, want %v: %s", tc.id, got, tc.wantIgnored, body)
		}
	}
}

func TestSetupConsultRuntimeWiresTheBridge(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	if s.dispatchBridge != nil {
		t.Fatal("a server without a bridge has a dispatch bridge")
	}
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	launcher := bridgemod.NewLauncher(bridgemod.LauncherOptions{StateDir: t.TempDir(), LeoVersion: "vtest", LeoBin: "/opt/leo", Log: io.Discard})
	router := &bridge.Router{Hub: hub}
	s.setupConsultRuntime(Options{Bridge: BridgeOptions{Router: router, Launcher: launcher}}, nil)
	if s.dispatchBridge == nil {
		t.Fatal("dispatch bridge not wired")
	}
	if s.bridgeRouter != router {
		t.Fatal("agent bridge router not kept")
	}
}

// The bridge does not translate tool events, and they are activity only, so
// a bridged dispatch's tool hooks are applied through the real handler (a
// long tool call would otherwise read stalled) while its turn events stay
// superseded.
func TestDispatchReportHTTPAppliesToolEventsForBridgedDispatch(t *testing.T) {
	s, _, _ := newModeTestServer(t)
	got := postDispatch(t, s, `{"template":"coding","prompt":"work","cwd":"/tmp","mode":"interactive"}`)
	id := got.Data.ID
	s.dispatchBridge = fakeDispatchBridge{id: true}
	seq := 0
	post := func(payload string) string {
		t.Helper()
		seq++
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/dispatch/"+id+"/report", strings.NewReader(fmt.Sprintf(`{"event_id":"e%d","payload":`, seq)+payload+`}`))
		req.SetPathValue("id", id)
		s.handleAPIDispatchReport(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	if body := post(`{"hook_event_name":"PreToolUse","tool_use_id":"t1"}`); strings.Contains(body, "superseded_by_bridge") {
		t.Fatalf("tool event superseded by the bridge: %s", body)
	}
	rec, err := s.consults.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, open := rec.OpenTools["t1"]; !open || rec.HookActivity.IsZero() {
		t.Fatalf("bridged tool event not applied: open=%v activity=%v", rec.OpenTools, rec.HookActivity)
	}

	if body := post(`{"hook_event_name":"Stop"}`); !strings.Contains(body, "superseded_by_bridge") {
		t.Fatalf("turn event not superseded: %s", body)
	}
	if rec, _ = s.consults.Get(id); len(rec.OpenTools) != 1 {
		t.Fatalf("superseded Stop was applied: %v", rec.OpenTools)
	}

	if body := post(`{"hook_event_name":"PostToolUse","tool_use_id":"t1"}`); strings.Contains(body, "superseded_by_bridge") {
		t.Fatalf("PostToolUse superseded: %s", body)
	}
	if rec, _ = s.consults.Get(id); len(rec.OpenTools) != 0 {
		t.Fatalf("open tools after PostToolUse: %v", rec.OpenTools)
	}
}
