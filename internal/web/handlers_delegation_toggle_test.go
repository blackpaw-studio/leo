package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestAPIDispatchRoleFailsLoudlyWhenDelegationDisabled(t *testing.T) {
	s, path := delegationServer(t)
	cfg := loadConfigFile(t, path)
	cfg.Delegation.SetEnabled(false)
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	code, got := dispatchRole(t, s, `{"role":"implement","prompt":"x","cwd":"/tmp"}`)
	want := `delegation is disabled (enable it with \"leo delegation enable\" or the web Delegation page)`
	if code != http.StatusBadRequest || !strings.Contains(got, want) {
		t.Fatalf("code=%d body=%s", code, got)
	}
	if code, got := dispatchRole(t, s, `{"template":"one","prompt":"x","cwd":"/tmp"}`); code != http.StatusOK {
		t.Fatalf("template dispatch must be unaffected: %d %s", code, got)
	}
	w := httptest.NewRecorder()
	s.handleAPIDelegation(w, httptest.NewRequest(http.MethodGet, "/api/delegation", nil))
	if !strings.Contains(w.Body.String(), "delegation is disabled") {
		t.Fatalf("leo_delegation status = %s", w.Body.String())
	}
}

func TestDelegationToggleHandler(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationTestConfig())
	w := postDelegation(t, s.handleDelegationEnabled, "enabled=false")
	if w.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("toggle must refresh the page: %s", w.Body.String())
	}
	loaded := loadConfigFile(t, path)
	if loaded.Delegation.IsEnabled() || loaded.Delegation.Profiles["p"].Roles["implement"].Template != "one" {
		t.Fatalf("disable: %+v", loaded.Delegation)
	}
	page := getDelegationPage(t, s)
	if !strings.Contains(page, "Delegation is off") || !strings.Contains(page, "is-disabled") || !strings.Contains(page, `name="enabled" value="true"`) {
		t.Fatal("disabled page must show the banner, dim the editor, and offer enable")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if w := postDelegation(t, s.handleDelegationEnabled, "enabled=maybe"); !strings.Contains(w.Body.String(), "enabled must be true or false") {
		t.Fatalf("bad value response = %s", w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid toggle changed leo.yaml")
	}
	postDelegation(t, s.handleDelegationEnabled, "enabled=true")
	if !loadConfigFile(t, path).Delegation.IsEnabled() {
		t.Fatal("enable did not persist")
	}
	if page := getDelegationPage(t, s); strings.Contains(page, "Delegation is off") || !strings.Contains(page, `name="enabled" value="false"`) {
		t.Fatal("enabled page must hide the banner and offer disable")
	}
}
