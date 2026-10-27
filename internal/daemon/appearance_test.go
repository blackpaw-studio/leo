package daemon

import (
	"path/filepath"
	"testing"
)

// Every config change reaches the daemon as a reload (the config API, the
// rules route, the web forms and POST /config/reload all call it), so the
// reload is where appearance is republished.
func TestReloadConfigRepublishesAppearance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	saveClients(t, path, nil)
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	mgr := &fakeAgentManager{}
	s.SetAgentManager(mgr)

	if err := s.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	if mgr.republished != 1 {
		t.Fatalf("RepublishAppearance called %d times, want 1", mgr.republished)
	}
}
