package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web"
)

// fakeClientSetter records the policies a reload swaps in.
type fakeClientSetter struct {
	mu    sync.Mutex
	calls [][]web.ClientPolicy
}

func (f *fakeClientSetter) SetClients(cs []web.ClientPolicy) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cs)
}

func (f *fakeClientSetter) last(t *testing.T) []web.ClientPolicy {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("SetClients was never called")
	}
	return f.calls[len(f.calls)-1]
}

func clientNames(cs []web.ClientPolicy) []string {
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.Name)
	}
	slices.Sort(names)
	return names
}

func saveClients(t *testing.T, path string, clients map[string]config.APIClientConfig) {
	t.Helper()
	cfg := &config.Config{HomePath: filepath.Dir(path), APIClients: clients}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestReloadConfigSwapsClientPoliciesIntoTheWebServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	saveClients(t, path, nil)
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	sink := &fakeClientSetter{}
	s.setClientSink(sink)

	saveClients(t, path, map[string]config.APIClientConfig{
		"pipe":  {CanPost: []string{"pipe-*"}},
		"scout": {CanMessage: []string{"rocket"}},
	})
	if err := s.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	got := sink.last(t)
	if !slices.Equal(clientNames(got), []string{"pipe", "scout"}) {
		t.Fatalf("clients after add = %v", clientNames(got))
	}
	for _, c := range got {
		if c.Token == "" {
			t.Errorf("client %q has no token", c.Name)
		}
		if c.Name == "pipe" && !slices.Equal(c.CanPost, []string{"pipe-*"}) {
			t.Errorf("pipe CanPost = %v", c.CanPost)
		}
		if c.Name == "scout" && !slices.Equal(c.CanMessage, []string{"rocket"}) {
			t.Errorf("scout CanMessage = %v", c.CanMessage)
		}
	}

	saveClients(t, path, map[string]config.APIClientConfig{"scout": {CanMessage: []string{"rocket"}}})
	if err := s.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	if got := sink.last(t); !slices.Equal(clientNames(got), []string{"scout"}) {
		t.Fatalf("clients after removal = %v, want only scout", clientNames(got))
	}
}

func TestReloadConfigKeepsOldClientsWhenTheConfigWillNotLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	saveClients(t, path, map[string]config.APIClientConfig{"pipe": {CanPost: []string{"pipe-*"}}})
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	sink := &fakeClientSetter{}
	s.setClientSink(sink)

	if err := os.WriteFile(path, []byte("defaults: [not a map"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadConfig(); err == nil {
		t.Fatal("ReloadConfig on a broken config = nil, want an error")
	}
	if len(sink.calls) != 0 {
		t.Fatalf("a failed load swapped clients: %v", sink.calls)
	}
}

func TestSocketConfigReloadAlsoSwapsClients(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	saveClients(t, path, map[string]config.APIClientConfig{"pipe": {CanPost: []string{"pipe-*"}}})
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	sink := &fakeClientSetter{}
	s.setClientSink(sink)

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/config/reload", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := sink.last(t); !slices.Equal(clientNames(got), []string{"pipe"}) {
		t.Fatalf("clients = %v", clientNames(got))
	}
}

func TestSocketConfigReloadReportsAReloadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(path, []byte("defaults: [not a map"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/config/reload", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestReloadConfigWithoutAWebServerDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	saveClients(t, path, map[string]config.APIClientConfig{"pipe": {CanPost: []string{"pipe-*"}}})
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	if err := s.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
}
