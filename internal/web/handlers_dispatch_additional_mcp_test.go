package web

import (
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func dispatchBodyWithServers(servers string) string {
	return `{"from":"nightly-task","template":"coding","prompt":"work","cwd":"/tmp","additional_mcp_servers":` + servers + `}`
}

func postDispatchStatus(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(body)))
	return w.Code, w.Body.String()
}

func TestDispatchHandlerRejectsTooManyAdditionalMCPServers(t *testing.T) {
	s, rt, _ := newModeTestServer(t)
	names := make([]string, 17)
	for i := range names {
		names[i] = strconv.Quote("s" + strconv.Itoa(i))
	}

	code, body := postDispatchStatus(t, s, dispatchBodyWithServers("["+strings.Join(names, ",")+"]"))

	if code != 400 || !strings.Contains(body, "invalid additional_mcp_servers") || !strings.Contains(body, "17") {
		t.Fatalf("code=%d body=%s", code, body)
	}
	if rt.count() != 0 {
		t.Fatalf("a rejected request launched %d runs", rt.count())
	}
}

func TestDispatchHandlerRejectsSlashInAdditionalMCPServer(t *testing.T) {
	s, rt, _ := newModeTestServer(t)

	code, body := postDispatchStatus(t, s, dispatchBodyWithServers(`["a/b"]`))

	if code != 400 || !strings.Contains(body, "invalid additional_mcp_servers") || !strings.Contains(body, `a/b`) {
		t.Fatalf("code=%d body=%s", code, body)
	}
	if rt.count() != 0 {
		t.Fatalf("a rejected request launched %d runs", rt.count())
	}
}

func TestDispatchHandlerForwardsDedupedAdditionalMCPServers(t *testing.T) {
	s, rt, _ := newModeTestServer(t)

	postDispatch(t, s, dispatchBodyWithServers(`["chrome","other","chrome"]`))

	if rt.count() != 1 {
		t.Fatalf("launches=%d, want 1", rt.count())
	}
	rt.mu.Lock()
	got := rt.launches[0].AdditionalMCPServers
	rt.mu.Unlock()
	if !slices.Equal(got, []string{"chrome", "other"}) {
		t.Fatalf("AdditionalMCPServers = %v, want [chrome other]", got)
	}
}
