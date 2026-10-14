package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/drain"
)

type stubProber struct{ busy []drain.Probe }

func (p stubProber) BusyAgents(context.Context) ([]drain.Probe, error) { return p.busy, nil }

func (p stubProber) Probe(_ context.Context, name string) (drain.Probe, error) {
	for _, probe := range p.busy {
		if probe.Name == name {
			return probe, nil
		}
	}
	return drain.Probe{Name: name}, nil
}

type stubNotifier struct{}

func (stubNotifier) Message(context.Context, string, string, string) (drain.Delivery, error) {
	return drain.Delivery{Transport: "bridge"}, nil
}
func (stubNotifier) Holds(string, string) (bool, error) { return false, nil }

type stubCron struct{}

func (stubCron) Hold()          {}
func (stubCron) Release()       {}
func (stubCron) Held() []string { return nil }

type stubStore struct{}

func (stubStore) Write(drain.PendingResume) error { return nil }
func (stubStore) Remove() error                   { return nil }

// stoppedClock never fires, so Run never ticks on its own.
type stoppedClock struct{}

func (stoppedClock) Now() time.Time                       { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
func (stoppedClock) After(time.Duration) <-chan time.Time { return nil }

func drainServer(t *testing.T, busy ...drain.Probe) *Server {
	t.Helper()
	s := New(tmpSockPath(t, "d.sock"), "/nonexistent/leo.yaml", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.SetParentContext(ctx)
	s.SetDrain(drain.New(drain.Deps{
		Prober:   stubProber{busy: busy},
		Notifier: stubNotifier{},
		Cron:     stubCron{},
		Store:    stubStore{},
		Clock:    stoppedClock{},
	}))
	return s
}

func drainRequest(t *testing.T, s *Server, method, path, body string) (*httptest.ResponseRecorder, Response) {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s %s: decoding %q: %v", method, path, w.Body.String(), err)
	}
	return w, resp
}

func TestDrainRoutesWithoutManagerAre503(t *testing.T) {
	s := New(tmpSockPath(t, "d.sock"), "/nonexistent/leo.yaml", nil)
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/drain/busy", ""},
		{"POST", "/drain", `{"timeout_seconds":60}`},
		{"GET", "/drain", ""},
		{"POST", "/drain/commit", `{}`},
		{"POST", "/drain/cancel", `{}`},
	} {
		w, resp := drainRequest(t, s, route.method, route.path, route.body)
		if w.Code != http.StatusServiceUnavailable || resp.Code != ErrorCodeDrainUnavailable {
			t.Errorf("%s %s = %d %q, want 503 %s", route.method, route.path, w.Code, resp.Code, ErrorCodeDrainUnavailable)
		}
	}
}

func TestDrainBusyListsBusyAgents(t *testing.T) {
	busy := []drain.Probe{{Name: "a", Exists: true, Busy: true, Transport: "bridge"}}
	s := drainServer(t, busy...)
	w, resp := drainRequest(t, s, "GET", "/drain/busy", "")
	if w.Code != http.StatusOK || !resp.OK {
		t.Fatalf("GET /drain/busy = %d %+v", w.Code, resp)
	}
	var got []drain.Probe
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, busy) {
		t.Fatalf("busy = %+v, want %+v", got, busy)
	}
	if !strings.Contains(string(resp.Data), `"name":"a"`) || !strings.Contains(string(resp.Data), `"busy":true`) {
		t.Fatalf("wire shape = %s", resp.Data)
	}
}

func TestDrainBeginValidatesTimeout(t *testing.T) {
	s := drainServer(t)
	for _, body := range []string{`{"timeout_seconds":0}`, `{"timeout_seconds":86401}`, `{}`, `not json`} {
		if w, _ := drainRequest(t, s, "POST", "/drain", body); w.Code != http.StatusBadRequest {
			t.Errorf("POST /drain %s = %d, want 400", body, w.Code)
		}
	}
	if w, _ := drainRequest(t, s, "POST", "/drain", `{"timeout_seconds":86400}`); w.Code != http.StatusOK {
		t.Errorf("POST /drain at the 86400 bound = %d, want 200", w.Code)
	}
}

func TestDrainLifecycleOverIPC(t *testing.T) {
	s := drainServer(t, drain.Probe{Name: "a", Exists: true, Busy: true, Transport: "bridge"})
	w, resp := drainRequest(t, s, "POST", "/drain", `{"timeout_seconds":600}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /drain = %d %+v", w.Code, resp)
	}
	var st drain.Status
	if err := json.Unmarshal(resp.Data, &st); err != nil {
		t.Fatal(err)
	}
	if st.Phase != drain.PhaseDraining || st.TimeoutMinutes != 10 || len(st.Members) != 1 || st.Members[0].Name != "a" {
		t.Fatalf("begin status = %+v", st)
	}
	if w, resp := drainRequest(t, s, "POST", "/drain", `{"timeout_seconds":600}`); w.Code != http.StatusConflict || resp.Code != ErrorCodeDrainInProgress {
		t.Fatalf("second POST /drain = %d %q, want 409 %s", w.Code, resp.Code, ErrorCodeDrainInProgress)
	}
	if w, resp := drainRequest(t, s, "POST", "/drain/commit", `{"force":false}`); w.Code != http.StatusConflict || resp.Code != ErrorCodeDrainNotDrained {
		t.Fatalf("commit before drained = %d %q, want 409 %s", w.Code, resp.Code, ErrorCodeDrainNotDrained)
	}
	if w, _ := drainRequest(t, s, "GET", "/drain", ""); w.Code != http.StatusOK {
		t.Fatalf("GET /drain = %d", w.Code)
	}
	if w, resp := drainRequest(t, s, "POST", "/drain/commit", `{"force":true}`); w.Code != http.StatusOK {
		t.Fatalf("forced commit = %d %+v", w.Code, resp)
	}
	if w, resp := drainRequest(t, s, "POST", "/drain/cancel", `{}`); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %+v", w.Code, resp)
	}
	if w, resp := drainRequest(t, s, "POST", "/drain/cancel", `{}`); w.Code != http.StatusConflict || resp.Code != ErrorCodeDrainNone {
		t.Fatalf("cancel without drain = %d %q, want 409 %s", w.Code, resp.Code, ErrorCodeDrainNone)
	}
}

func TestDaemonDrainNotifierUnavailableUntilWeb(t *testing.T) {
	s := New(tmpSockPath(t, "d.sock"), "/nonexistent/leo.yaml", nil)
	n := s.DrainNotifier()
	if _, err := n.Message(context.Background(), "a", "x", ""); !errors.Is(err, drain.ErrNotifierUnavailable) {
		t.Fatalf("Message err = %v, want ErrNotifierUnavailable", err)
	}
	s.setDrainNotifier(stubNotifier{})
	if _, err := n.Message(context.Background(), "a", "x", ""); err != nil {
		t.Fatalf("Message after web started: %v", err)
	}
}
