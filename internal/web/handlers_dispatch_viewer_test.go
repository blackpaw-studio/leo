package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

type viewerTestReloader struct{ err error }

func (r viewerTestReloader) ReloadConfig() error { return r.err }
func viewerHandlerServer(t *testing.T, reload error) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leo.yaml")
	if err := os.WriteFile(path, []byte("tasks: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return &Server{configWriter: config.NewWriter(), configPath: path, apiToken: "operator", agentToken: "agent", reloader: viewerTestReloader{reload}, consults: consult.NewDispatcher(nil)}
}
func viewerRequest(path, body, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}
func TestCloseFinishedSessionIsolation(t *testing.T) {
	s := viewerHandlerServer(t, nil)
	recordState := t.TempDir()
	if err := os.MkdirAll(consult.Dir(recordState), 0700); err != nil {
		t.Fatal(err)
	}
	recorder := consult.NewFileRecorder(recordState)
	for _, rec := range []consult.Record{{ID: "target", Kind: "dispatch", Status: consult.StatusDone, CallerSessionID: "$1", ViewerWindowID: "@1"}, {ID: "other", Kind: "dispatch", Status: consult.StatusDone, CallerSessionID: "$2", ViewerWindowID: "@2"}} {
		if err := recorder.PersistRecord(rec); err != nil {
			t.Fatal(err)
		}
	}
	s.consults = consult.NewDispatcher(recorder)
	var closed []string
	s.consults.SetCloseFinishedViewer(func(rec consult.Record, _ func(string) error) (consult.Record, error) {
		closed = append(closed, rec.ID)
		return rec, nil
	})
	w := httptest.NewRecorder()
	s.handleDispatchViewerCloseFinished(w, viewerRequest("/api/dispatch/viewer/close-finished", `{"session_id":"$1"}`, "operator"))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(closed) != 1 || closed[0] != "target" {
		t.Fatalf("closed=%q", closed)
	}
}
func TestSaveViewerDefaultsRoundTrip(t *testing.T) {
	s := viewerHandlerServer(t, nil)
	w := httptest.NewRecorder()
	s.handleDispatchViewerSaveDefault(w, viewerRequest("/api/dispatch/viewer/save-default", `{"placement":"window","max_panes":6}`, "operator"))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DispatchViewerPlacement() != "window" || cfg.DispatchViewerMaxPanes() != 6 {
		t.Fatalf("viewer=%+v", cfg.Defaults.Dispatch.Viewer)
	}
}
func TestSaveViewerDefaultsRejectsInvalidOverrides(t *testing.T) {
	s := viewerHandlerServer(t, nil)
	before, _ := os.ReadFile(s.configPath)
	w := httptest.NewRecorder()
	s.handleDispatchViewerSaveDefault(w, viewerRequest("/api/dispatch/viewer/save-default", `{"max_panes":7}`, "operator"))
	after, _ := os.ReadFile(s.configPath)
	if w.Code != 400 || string(before) != string(after) {
		t.Fatalf("code=%d changed=%v", w.Code, string(before) != string(after))
	}
}
func TestSaveViewerDefaultsRejectsBlankPlacement(t *testing.T) {
	for _, value := range []string{"", "   "} {
		s := viewerHandlerServer(t, nil)
		w := httptest.NewRecorder()
		s.handleDispatchViewerSaveDefault(w, viewerRequest("/api/dispatch/viewer/save-default", `{"placement":"`+value+`"}`, "operator"))
		if w.Code != 400 {
			t.Fatalf("placement %q code=%d body=%s", value, w.Code, w.Body.String())
		}
	}
}
func TestSaveViewerDefaultsReloadFailure(t *testing.T) {
	s := viewerHandlerServer(t, errors.New("boom"))
	w := httptest.NewRecorder()
	s.handleDispatchViewerSaveDefault(w, viewerRequest("/api/dispatch/viewer/save-default", `{"placement":"window"}`, "operator"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reloaded":false`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestSaveViewerDefaultsOperatorOnly(t *testing.T) {
	s := viewerHandlerServer(t, nil)
	w := httptest.NewRecorder()
	s.handleDispatchViewerSaveDefault(w, viewerRequest("/api/dispatch/viewer/save-default", `{}`, "agent"))
	if w.Code != 403 {
		t.Fatalf("code=%d", w.Code)
	}
	s.trustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	w = httptest.NewRecorder()
	r := viewerRequest("/api/dispatch/viewer/save-default", `{}`, "agent")
	r.RemoteAddr = "10.1.2.3:1234"
	r.Header.Set("Remote-User", "evan")
	s.handleDispatchViewerSaveDefault(w, r)
	if w.Code != 200 {
		t.Fatalf("trusted proxy code=%d body=%s", w.Code, w.Body.String())
	}
}
