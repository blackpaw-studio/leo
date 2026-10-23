package web

import (
	"net/http"
	"testing"
)

// The socket and the web API mount the same service, so the same request must
// get the same bytes from both.
func TestSocketAndWebServeIdenticalBodies(t *testing.T) {
	svc, path := newConfigService(t, nil)
	socket := newAPIHarnessOver(t, svc, path, "")
	web := newAPIHarnessOver(t, svc, path, "/api/v1")

	reads := []string{
		"/config/schema", "/config/defaults", "/config/templates", "/config/templates/coder",
		"/config/tasks", "/config/tasks/heartbeat", "/config/environments", "/config/web",
		"/config/client", "/config/hosts", "/config/delegation", "/config/delegation/preview?profile=deep",
		"/tasks/heartbeat/history", "/config/tasks/heartbeat/prompt", "/service/status", "/cron/preview?expr=0+*+*+*+*",
		"/config/nope", "/config/templates/ghost",
	}
	for _, p := range reads {
		a, b := socket.do(http.MethodGet, p, "", nil), web.do(http.MethodGet, p, "", nil)
		if a.Status != b.Status || string(a.Raw) != string(b.Raw) {
			t.Errorf("GET %s differs:\n socket %d %s\n web    %d %s", p, a.Status, a.Raw, b.Status, b.Raw)
		}
	}

	stale := map[string]any{"values": map[string]any{"model": "opus"}}
	a := socket.do(http.MethodPut, "/config/defaults", "sha256:stale", stale)
	b := web.do(http.MethodPut, "/config/defaults", "sha256:stale", stale)
	if a.Status != http.StatusConflict || a.Status != b.Status || string(a.Raw) != string(b.Raw) {
		t.Errorf("409 differs:\n socket %d %s\n web    %d %s", a.Status, a.Raw, b.Status, b.Raw)
	}
}
