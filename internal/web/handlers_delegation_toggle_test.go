package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"gopkg.in/yaml.v3"
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

// A config written before the switch existed must not gain an `enabled:` key
// just because an unrelated web save round-trips it.
func TestUnrelatedWebSaveKeepsEnabledKeyAbsent(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationTestConfig())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if delegationEnabledKeyPresent(t, before) {
		t.Fatalf("precondition: fixture already has delegation.enabled:\n%s", before)
	}
	w := postDelegation(t, s.handleDelegationUseFor, "role=implement&use_for=changed")
	if !strings.Contains(w.Body.String(), "saved") {
		t.Fatalf("save failed: %s", w.Body.String())
	}
	postDelegation(t, s.handleDelegationProfileAdd, "name=extra")
	loaded := loadConfigFile(t, path)
	if loaded.Delegation.Enabled != nil {
		t.Fatalf("unrelated save wrote delegation.enabled = %v", *loaded.Delegation.Enabled)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if delegationEnabledKeyPresent(t, after) {
		t.Fatalf("delegation.enabled key appeared:\n%s", after)
	}
}

func delegationEnabledKeyPresent(t *testing.T, data []byte) bool {
	t.Helper()
	var raw map[string]map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	_, ok := raw["delegation"]["enabled"]
	return ok
}

// Activation success must not land in the dialog's role="alert" error slot.
func TestDelegationActivationSuccessIsNotAnAlert(t *testing.T) {
	cfg := delegationTestConfig()
	cfg.Delegation.Profiles["next"] = config.Profile{Roles: map[string]config.RoleTarget{"implement": {Template: "two"}}}
	s, _ := newTestServerWithConfigFile(t, cfg)
	w := postDelegation(t, s.handleDelegationActive, "profile=next")
	if w.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("activation must refresh: %v", w.Header())
	}
	if retarget := w.Header().Get("HX-Retarget"); retarget != "#flash-container" {
		t.Fatalf("success must be retargeted out of the alert slot, got HX-Retarget=%q", retarget)
	}
	w = postDelegation(t, s.handleDelegationActive, "profile=missing")
	if w.Header().Get("HX-Retarget") != "" || !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("errors stay in the dialog slot: %v %s", w.Header(), w.Body.String())
	}
}
