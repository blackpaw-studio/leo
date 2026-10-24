package web

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/consult"
)

func (h *apiHarness) delegationDoc() map[string]any {
	h.t.Helper()
	res := h.do(http.MethodGet, "/config/delegation", "", nil)
	wantStatus(h.t, res, http.StatusOK)
	return res.data()
}

func TestDelegationDocument(t *testing.T) {
	h := newAPIHarness(t)
	h.svc.SetRecentConsults(func() []consult.Record {
		return []consult.Record{{ID: "c1", Role: "coder", Template: "coder"}, {ID: "c2"}, {ID: "c3", Role: "coder"}}
	})
	res := h.do(http.MethodGet, "/config/delegation", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["revision"] != h.revision() || res.Header.Get("ETag") != `"`+h.revision()+`"` {
		t.Errorf("revision/ETag = %v / %q", d["revision"], res.Header.Get("ETag"))
	}
	if d["configured"] != true || d["enabled"] != true || d["active_profile"] != "fast" {
		t.Errorf("header = %v", d)
	}
	if hide := d["hide_native_agents"].([]any); len(hide) == 0 || d["hide_native_agents_is_default"] != true {
		t.Errorf("hide = %v default=%v", hide, d["hide_native_agents_is_default"])
	}
	tmpls := d["templates"].([]any)
	if len(tmpls) != 2 || tmpls[0].(map[string]any)["name"] != "coder" || tmpls[0].(map[string]any)["model"] != "opus" {
		t.Errorf("templates = %v", tmpls)
	}
	roles := d["roles"].([]any)
	if len(roles) != 1 || roles[0].(map[string]any)["name"] != "coder" || roles[0].(map[string]any)["declared"] != true || roles[0].(map[string]any)["use_for"] != "writing code" {
		t.Errorf("roles = %v", roles)
	}
	profiles := d["profiles"].([]any)
	if len(profiles) != 2 || profiles[0].(map[string]any)["name"] != "deep" || profiles[1].(map[string]any)["active"] != true {
		t.Fatalf("profiles = %v", profiles)
	}
	cell := profiles[1].(map[string]any)["roles"].(map[string]any)["coder"].(map[string]any)
	if cell["template"] != "reviewer" || cell["inherited_model"] != "haiku" || cell["model"] != "" {
		t.Errorf("cell = %v", cell)
	}
	if _, ok := d["warnings"].([]any); !ok {
		t.Errorf("warnings must be an array, got %v", d["warnings"])
	}
	consults := d["recent_consults"].([]any)
	if len(consults) != 2 {
		t.Errorf("recent_consults = %v, want only role-routed records", consults)
	}
}

func TestDelegationDocumentWithoutABlock(t *testing.T) {
	h := newAPIHarness(t)
	noDelegation, _, _ := strings.Cut(configAPIFixtureYAML, "delegation:")
	if err := os.WriteFile(h.path, []byte(noDelegation), 0o600); err != nil {
		t.Fatal(err)
	}
	d := h.delegationDoc()
	if d["configured"] != false {
		t.Errorf("configured = %v, want false", d["configured"])
	}
	res := h.do(http.MethodPut, "/config/delegation/enabled", h.revision(), map[string]any{"enabled": false})
	wantStatus(t, res, http.StatusNotFound)
	wantCode(t, res, CodeDelegationMissing)
	res = h.do(http.MethodGet, "/config/delegation/preview?profile=deep", "", nil)
	wantStatus(t, res, http.StatusNotFound)
	wantCode(t, res, CodeDelegationMissing)
}

func TestDelegationCellWrite(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodPut, "/config/delegation/profiles/fast/roles/coder", h.revision(),
		map[string]any{"template": "coder", "model": "opus", "effort": "high"})
	wantStatus(t, res, http.StatusOK)
	if res.data()["revision"] != h.revision() {
		t.Error("response revision is not the new on-disk revision")
	}
	cell := h.loadConfig().Delegation.Profiles["fast"].Roles["coder"]
	if cell.Template != "coder" || cell.Model != "opus" || cell.Effort != "high" {
		t.Errorf("cell = %+v", cell)
	}
	if _, ok := res.data()["item"].(map[string]any); !ok {
		t.Errorf("write response must carry the refreshed delegation document")
	}

	// An empty template deletes the cell.
	wantStatus(t, h.do(http.MethodPut, "/config/delegation/profiles/deep/roles/coder", h.revision(), map[string]any{"template": ""}), http.StatusOK)
	if _, ok := h.loadConfig().Delegation.Profiles["deep"].Roles["coder"]; ok {
		t.Error("cell not deleted")
	}
}

func TestDelegationCellInvalidEffortIs422OnEffort(t *testing.T) {
	h := newAPIHarness(t)
	before := h.fileBytes()
	res := h.do(http.MethodPut, "/config/delegation/profiles/fast/roles/coder", h.revision(),
		map[string]any{"template": "coder", "effort": "ludicrous"})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if f := res.errorFields(); len(f) != 1 || f[0]["field"] != "effort" {
		t.Errorf("errors = %v, want one on effort", f)
	}
	if h.fileBytes() != before {
		t.Error("an invalid cell changed leo.yaml")
	}
	res = h.do(http.MethodPut, "/config/delegation/profiles/fast/roles/coder", h.revision(), map[string]any{"template": "ghost"})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if f := res.errorFields(); len(f) != 1 || f[0]["field"] != "template" {
		t.Errorf("errors = %v, want one on template", f)
	}
	wantStatus(t, h.do(http.MethodPut, "/config/delegation/profiles/ghost/roles/coder", h.revision(), map[string]any{"template": "coder"}), http.StatusNotFound)
}

func TestDelegationSimpleWrites(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.do(http.MethodPut, "/config/delegation/roles/coder", h.revision(), map[string]any{"use_for": "everything"}), http.StatusOK)
	if h.loadConfig().Delegation.Roles["coder"].UseFor != "everything" {
		t.Error("use_for not saved")
	}
	wantStatus(t, h.do(http.MethodPut, "/config/delegation/roles/ghost", h.revision(), map[string]any{"use_for": "x"}), http.StatusNotFound)

	wantStatus(t, h.do(http.MethodPut, "/config/delegation/active", h.revision(), map[string]any{"profile": "deep"}), http.StatusOK)
	if h.loadConfig().Delegation.ActiveProfile != "deep" {
		t.Error("active profile not switched")
	}
	wantStatus(t, h.do(http.MethodPut, "/config/delegation/active", h.revision(), map[string]any{"profile": "ghost"}), http.StatusNotFound)

	wantStatus(t, h.do(http.MethodPut, "/config/delegation/enabled", h.revision(), map[string]any{"enabled": false}), http.StatusOK)
	if h.loadConfig().Delegation.IsEnabled() {
		t.Error("delegation not disabled")
	}
	wantStatus(t, h.do(http.MethodPut, "/config/delegation/enabled", h.revision(), map[string]any{}), http.StatusBadRequest)

	wantStatus(t, h.do(http.MethodPut, "/config/delegation/hide-agents", h.revision(), map[string]any{"names": []string{"Explore", "Plan"}}), http.StatusOK)
	if got := h.loadConfig().Delegation.HiddenNativeAgents(); len(got) != 2 {
		t.Errorf("hidden = %v", got)
	}
	d := h.delegationDoc()
	if d["hide_native_agents_is_default"] != false {
		t.Errorf("is_default = %v after an explicit list", d["hide_native_agents_is_default"])
	}
	wantStatus(t, h.do(http.MethodPut, "/config/delegation/hide-agents", h.revision(), map[string]any{"names": []string{"bad name"}}), http.StatusUnprocessableEntity)
}

func TestDelegationWritesNeedAFreshRevision(t *testing.T) {
	h := newAPIHarness(t)
	before := h.fileBytes()
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/config/delegation/active", map[string]any{"profile": "deep"}},
		{http.MethodPut, "/config/delegation/profiles/fast/roles/coder", map[string]any{"template": "coder"}},
		{http.MethodPost, "/config/delegation/roles", map[string]any{"name": "r2", "template": "coder"}},
		{http.MethodPost, "/config/delegation/profiles", map[string]any{"name": "p2"}},
		{http.MethodDelete, "/config/delegation/profiles/deep", nil},
	} {
		wantStatus(t, h.do(tc.method, tc.path, "", tc.body), http.StatusPreconditionRequired)
		res := h.do(tc.method, tc.path, "sha256:stale", tc.body)
		wantStatus(t, res, http.StatusConflict)
		wantCode(t, res, CodeRevisionMismatch)
	}
	if h.fileBytes() != before {
		t.Error("a refused write changed leo.yaml")
	}
}

func TestDelegationRoleAddRenameDelete(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodPost, "/config/delegation/roles", h.revision(), map[string]any{"name": "reviewing", "template": "reviewer"})
	wantStatus(t, res, http.StatusCreated)
	d := h.loadConfig().Delegation
	if _, ok := d.Roles["reviewing"]; !ok || d.Profiles["fast"].Roles["reviewing"].Template != "reviewer" {
		t.Errorf("role not added: %+v", d)
	}
	wantStatus(t, h.do(http.MethodPost, "/config/delegation/roles", h.revision(), map[string]any{"name": "coder", "template": "coder"}), http.StatusUnprocessableEntity)
	res = h.do(http.MethodPost, "/config/delegation/roles", h.revision(), map[string]any{"name": "x", "template": "ghost"})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if f := res.errorFields(); f[0]["field"] != "template" {
		t.Errorf("errors = %v", f)
	}

	res = h.do(http.MethodPost, "/config/delegation/roles/reviewing/rename", h.revision(), map[string]any{"new_name": "checking"})
	wantStatus(t, res, http.StatusOK)
	if _, ok := h.loadConfig().Delegation.Roles["checking"]; !ok {
		t.Error("role not renamed")
	}
	res = h.do(http.MethodPost, "/config/delegation/roles/checking/rename", h.revision(), map[string]any{"new_name": "coder"})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if f := res.errorFields(); f[0]["field"] != "new_name" {
		t.Errorf("errors = %v", f)
	}
	wantStatus(t, h.do(http.MethodPost, "/config/delegation/roles/ghost/rename", h.revision(), map[string]any{"new_name": "zzz"}), http.StatusNotFound)

	wantStatus(t, h.do(http.MethodDelete, "/config/delegation/roles/checking", h.revision(), nil), http.StatusOK)
	wantStatus(t, h.do(http.MethodDelete, "/config/delegation/roles/ghost", h.revision(), nil), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodDelete, "/config/delegation/roles/coder", h.revision(), nil), http.StatusUnprocessableEntity)
}

func TestDelegationProfileAddRenameDuplicateDelete(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.do(http.MethodPost, "/config/delegation/profiles", h.revision(), map[string]any{"name": "cheap"}), http.StatusCreated)
	wantStatus(t, h.do(http.MethodPost, "/config/delegation/profiles", h.revision(), map[string]any{"name": "cheap"}), http.StatusUnprocessableEntity)

	wantStatus(t, h.do(http.MethodPost, "/config/delegation/profiles/deep/duplicate", h.revision(), map[string]any{"new_name": "deeper"}), http.StatusCreated)
	if got := h.loadConfig().Delegation.Profiles["deeper"].Roles["coder"].Template; got != "coder" {
		t.Errorf("duplicate lost its cells: %q", got)
	}
	wantStatus(t, h.do(http.MethodPost, "/config/delegation/profiles/deep/duplicate", h.revision(), map[string]any{"new_name": "fast"}), http.StatusUnprocessableEntity)
	wantStatus(t, h.do(http.MethodPost, "/config/delegation/profiles/ghost/duplicate", h.revision(), map[string]any{"new_name": "g"}), http.StatusNotFound)

	wantStatus(t, h.do(http.MethodPost, "/config/delegation/profiles/cheap/rename", h.revision(), map[string]any{"new_name": "thrifty"}), http.StatusOK)
	wantStatus(t, h.do(http.MethodPost, "/config/delegation/profiles/fast/rename", h.revision(), map[string]any{"new_name": "quick"}), http.StatusOK)
	if h.loadConfig().Delegation.ActiveProfile != "quick" {
		t.Error("renaming the active profile must move active_profile")
	}

	wantStatus(t, h.do(http.MethodDelete, "/config/delegation/profiles/thrifty", h.revision(), nil), http.StatusOK)
	wantStatus(t, h.do(http.MethodDelete, "/config/delegation/profiles/quick", h.revision(), nil), http.StatusUnprocessableEntity)
	wantStatus(t, h.do(http.MethodDelete, "/config/delegation/profiles/ghost", h.revision(), nil), http.StatusNotFound)
}

func TestDelegationPreview(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/config/delegation/preview?profile=deep", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["profile"] != "deep" {
		t.Errorf("profile = %v", d["profile"])
	}
	changes := d["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	c := changes[0].(map[string]any)
	if c["role"] != "coder" || c["kind"] != "changed" || c["before"].(map[string]any)["template"] != "reviewer" || c["after"].(map[string]any)["template"] != "coder" {
		t.Errorf("change = %v", c)
	}
	if missing := d["missing"].([]any); len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	wantStatus(t, h.do(http.MethodGet, "/config/delegation/preview?profile=ghost", "", nil), http.StatusNotFound)
}
