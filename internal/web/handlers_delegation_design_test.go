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

func delegationDesignConfig() *config.Config {
	cfg := delegationTestConfig()
	cfg.Templates["one"] = config.TemplateConfig{Model: "tmpl-model"}
	return cfg
}

func getDelegationPage(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/delegation", nil)
	authorizeTestRequest(req)
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	return w.Body.String()
}

func TestDelegationPageIsCompact(t *testing.T) {
	s, _ := newTestServerWithConfigFile(t, delegationDesignConfig())
	page := getDelegationPage(t, s)
	if strings.Contains(page, ">save<") {
		t.Fatal("page still renders per-cell save buttons")
	}
	if !strings.Contains(page, `hx-trigger="change`) {
		t.Fatal("grid cells do not autosave on change")
	}
	if !strings.Contains(page, `<details class="menu`) {
		t.Fatal("rename/remove actions are not in a menu")
	}
	if !strings.Contains(page, `placeholder="tmpl-model"`) {
		t.Fatal("grid does not show the inherited model as placeholder")
	}
	if !strings.Contains(page, `class="pill ok">active`) {
		t.Fatal("active profile badge missing")
	}
}

// Autosave must never replace the inputs: swapping the form drops focus and
// clobbers in-flight typing. Responses carry only the status indicator plus an
// out-of-band message slot.
func TestDelegationCellAutosaveKeepsInputs(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationDesignConfig())
	page := getDelegationPage(t, s)
	if !strings.Contains(page, `hx-target="find .cell-status"`) || !strings.Contains(page, `data-model="tmpl-model"`) {
		t.Fatal("cell form must target only its status and expose template models for placeholders")
	}
	w := postDelegation(t, s.handleDelegationCell, "profile=p&role=implement&template=one&model=&effort=")
	body := w.Body.String()
	if w.Header().Get("HX-Refresh") != "" || strings.Contains(body, "<input") || strings.Contains(body, "<select") {
		t.Fatalf("autosave response must not re-render inputs: %s", body)
	}
	if !strings.Contains(body, "cell-status ok") || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("cell response = %s", body)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w = postDelegation(t, s.handleDelegationCell, "profile=p&role=implement&template=missing")
	body = w.Body.String()
	if !strings.Contains(body, "cell-status err") || !strings.Contains(body, `class="cell-msg"`) || !strings.Contains(body, "missing") || strings.Contains(body, "<input") {
		t.Fatalf("error response = %s", body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid cell edit changed leo.yaml")
	}
}

func TestDelegationUseForErrorIsVisible(t *testing.T) {
	s, _ := newTestServerWithConfigFile(t, delegationDesignConfig())
	w := postDelegation(t, s.handleDelegationUseFor, "role=ghost&use_for=x")
	if !strings.Contains(w.Body.String(), `class="cell-msg"`) || !strings.Contains(w.Body.String(), "ghost") {
		t.Fatalf("use_for error must be visible: %s", w.Body.String())
	}
}

func TestDelegationActivationErrorsRenderInsideDialog(t *testing.T) {
	cfg := delegationDesignConfig()
	cfg.Delegation.Profiles["next"] = config.Profile{Roles: map[string]config.RoleTarget{"implement": {Template: "two"}}}
	s, _ := newTestServerWithConfigFile(t, cfg)
	w := httptest.NewRecorder()
	s.handleDelegationPreview(w, httptest.NewRequest(http.MethodGet, "/web/delegation/preview?profile=next", nil))
	body := w.Body.String()
	if !strings.Contains(body, `id="delegation-preview-error"`) || !strings.Contains(body, `hx-target="#delegation-preview-error"`) {
		t.Fatalf("activation errors must render inside the dialog: %s", body)
	}
}

func TestDelegationCellEscapesSubmittedValues(t *testing.T) {
	s, _ := newTestServerWithConfigFile(t, delegationDesignConfig())
	w := postDelegation(t, s.handleDelegationCell, "profile=p&role=implement&template=one&model=%3Cimg+src%3Dx%3E")
	if strings.Contains(w.Body.String(), "<img src=x>") {
		t.Fatalf("cell response is not escaped: %s", w.Body.String())
	}
}

func TestDelegationUseForAutosaveReportsStatus(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationDesignConfig())
	w := postDelegation(t, s.handleDelegationUseFor, "role=implement&use_for=ship+code")
	if w.Header().Get("HX-Refresh") != "" || !strings.Contains(w.Body.String(), "cell-status ok") {
		t.Fatalf("use_for response = %q headers=%v", w.Body.String(), w.Header())
	}
	if got := loadConfigFile(t, path).Delegation.Roles["implement"].UseFor; got != "ship code" {
		t.Fatalf("use_for = %q", got)
	}
}
