package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Permissions live on a nested struct the flat field registry cannot reach,
// so they are rendered from their own section and appended to the template
// form. Without that wiring the inputs simply never appear.
func TestTemplateEditPageRendersPermissionFields(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest(http.MethodGet, "/config/templates/coding", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, key := range []string{"deny_tools", "can_message", "can_spawn", "can_consult"} {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("template edit page is missing the %q input", key)
		}
	}
}

func TestTemplateSaveRoundTripsPermissions(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)
	form := templateFormBase(t, s, "coding")
	form.Set("deny_tools", "leo_spawn_agent, leo_stop_agent")
	form.Set("can_message", "rocket, scout-*")
	form.Set("can_spawn", "coding")
	form.Set("can_consult", "coding")

	w := postForm(t, s, "/web/config/template/coding", form)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}

	perms := reloadTestConfig(t, dir).Templates["coding"].Permissions
	if !perms.DeniesTool("leo_spawn_agent") || !perms.DeniesTool("leo_stop_agent") {
		t.Errorf("deny_tools not saved: %+v", perms)
	}
	if perms.AllowsMessage("olympus") || !perms.AllowsMessage("scout-leo") {
		t.Errorf("can_message not saved: %+v", perms)
	}
	if !perms.AllowsSpawn("coding") || perms.AllowsSpawn("other") {
		t.Errorf("can_spawn not saved: %+v", perms)
	}
	if !perms.AllowsConsult("coding") || perms.AllowsConsult("other") {
		t.Errorf("can_consult not saved: %+v", perms)
	}
}

// Clearing the inputs must actually lift the restriction, not leave the
// previous values in place — a save that silently keeps a stale deny list
// would be worse than no UI at all.
func TestTemplateSaveClearsPermissions(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)

	form := templateFormBase(t, s, "coding")
	form.Set("deny_tools", "leo_spawn_agent")
	if w := postForm(t, s, "/web/config/template/coding", form); w.Code != http.StatusOK {
		t.Fatalf("seed save: %d, body=%s", w.Code, readBody(t, w))
	}

	cleared := templateFormBase(t, s, "coding")
	cleared.Set("deny_tools", "")
	if w := postForm(t, s, "/web/config/template/coding", cleared); w.Code != http.StatusOK {
		t.Fatalf("clearing save: %d, body=%s", w.Code, readBody(t, w))
	}

	perms := reloadTestConfig(t, dir).Templates["coding"].Permissions
	if !perms.IsZero() {
		t.Errorf("clearing the form must lift every restriction, got %+v", perms)
	}
}

// A template save runs Config.Validate() before writing, so a misspelled tool
// name must be reported rather than silently persisted as a no-op.
func TestTemplateSaveRejectsUnknownDenyTool(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)
	form := templateFormBase(t, s, "coding")
	form.Set("deny_tools", "leo_spwan_agent")

	w := postForm(t, s, "/web/config/template/coding", form)
	if w.Code == http.StatusOK && !strings.Contains(readBody(t, w), "leo_spwan_agent") {
		t.Errorf("expected the save to report the bad tool name, got %d: %s", w.Code, readBody(t, w))
	}
	if perms := reloadTestConfig(t, dir).Templates["coding"].Permissions; !perms.IsZero() {
		t.Errorf("an invalid save must not persist: %+v", perms)
	}
}
