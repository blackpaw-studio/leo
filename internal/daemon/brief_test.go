package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/brief"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func saveBriefConfig(t *testing.T, path string, enabled bool) {
	t.Helper()
	if err := config.Save(path, &config.Config{HomePath: filepath.Dir(path), Brief: config.BriefConfig{Enabled: enabled}}); err != nil {
		t.Fatal(err)
	}
}

// The unix socket is the operator's own: it sees briefs, while briefs are on.
func TestSocketStateCarriesBriefsWhileOn(t *testing.T) {
	for _, on := range []bool{true, false} {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "leo.yaml")
		saveBriefConfig(t, cfgPath, on)
		s := New(filepath.Join(dir, "leo.sock"), cfgPath, nil)
		s.SetAgentManager(&fakeAgentManager{records: []agent.Record{{Name: "local", Status: "running", Brief: &brief.Brief{Goal: "ship it", UpdatedAt: time.Now()}}}})
		w := httptest.NewRecorder()

		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/state", nil))

		if got := strings.Contains(w.Body.String(), `"goal":"ship it"`); got != on {
			t.Errorf("enabled=%v: brief on the socket = %v (%s)", on, got, w.Body)
		}
	}
}

func TestSocketEventsCarryAgentBriefs(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "leo.sock"), filepath.Join(dir, "leo.yaml"), nil)
	bus := observe.NewBus()
	s.SetObservability(bus, nil, nil, nil, "test")
	clock := &daemonFakeClock{now: time.Unix(100, 0), ticker: &daemonFakeTicker{ch: make(chan time.Time)}}
	s.observeClock = clock
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	w := newStreamingRecorder()
	go func() {
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx))
		close(done)
	}()
	<-w.flushed // hello
	bus.Publish(observe.Event{Type: observe.EventAgentBrief, Payload: &observe.AgentBriefPayload{Agent: "local", Brief: &brief.Brief{Goal: "ship it"}}})
	<-w.flushed
	cancel()
	<-done
	if got := w.String(); !strings.Contains(got, "event: agent_brief\n") || !strings.Contains(got, `"goal":"ship it"`) {
		t.Fatalf("stream = %q", got)
	}
}

func TestReloadPublishesBriefConfigChangedOnlyWhenTheSwitchFlips(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	saveBriefConfig(t, cfgPath, false)
	s := New(filepath.Join(dir, "leo.sock"), cfgPath, nil)
	bus := observe.NewBus()
	s.SetObservability(bus, nil, nil, nil, "test")
	events, cancel, _ := bus.Subscribe(8)
	defer cancel()

	reload := func(enabled bool) *observe.BriefConfigChangedPayload {
		t.Helper()
		saveBriefConfig(t, cfgPath, enabled)
		if err := s.ReloadConfig(); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case ev := <-events:
				if p, ok := ev.Payload.(*observe.BriefConfigChangedPayload); ok && ev.Type == observe.EventBriefConfigChanged {
					return p
				}
			default:
				return nil
			}
		}
	}

	if p := reload(true); p == nil || !p.Enabled {
		t.Fatalf("flip on = %+v, want enabled: true", p)
	}
	if p := reload(true); p != nil {
		t.Fatalf("an unchanged reload published %+v", p)
	}
	if p := reload(false); p == nil || p.Enabled {
		t.Fatalf("flip off = %+v, want enabled: false", p)
	}
	raw, _ := json.Marshal(&observe.BriefConfigChangedPayload{})
	if strings.Contains(string(raw), "brief") {
		t.Fatal("the config event must carry no brief text")
	}
}
