package web

import (
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
