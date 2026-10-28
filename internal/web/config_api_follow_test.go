package web

import (
	"net/http"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/follow"
)

func TestConfigFollowStatusRoute(t *testing.T) {
	h := newFollowerAPIHarness(t, nil)
	res := h.do(http.MethodGet, "/config/follow", "", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Status, res.Raw)
	}
	d := res.data()
	if d["following"] != true || d["host"] != "lead" || d["lead_revision"] != "sha256:leadrev" {
		t.Fatalf("data = %v", d)
	}
	invalid, _ := d["invalid"].(map[string]any)
	if tmpl, _ := invalid["template"].(map[string]any); tmpl["bad"] == nil {
		t.Fatalf("invalid = %v", invalid)
	}
}

func TestConfigFollowSyncRoute(t *testing.T) {
	syncer := &fakeSyncer{out: follow.Outcome{Changed: true}}
	h := newFollowerAPIHarness(t, syncer)
	res := h.do(http.MethodPost, "/config/follow/sync", "", nil)
	if res.Status != http.StatusOK || syncer.calls != 1 || res.data()["changed"] != true {
		t.Fatalf("sync = %d %s (calls %d)", res.Status, res.Raw, syncer.calls)
	}
	if st, _ := res.data()["status"].(map[string]any); st["host"] != "lead" {
		t.Fatalf("status = %v", res.data())
	}

	syncer.err = &config.FollowError{Code: config.FollowUnreachable, Detail: "refused"}
	res = h.do(http.MethodPost, "/config/follow/sync", "", nil)
	if res.Status != http.StatusBadGateway || res.Body["code"] != "unreachable" {
		t.Fatalf("failed sync = %d %s", res.Status, res.Raw)
	}
}

func TestConfigFollowSyncWithoutLoopOrFollow(t *testing.T) {
	h := newFollowerAPIHarness(t, nil)
	if res := h.do(http.MethodPost, "/config/follow/sync", "", nil); res.Status != http.StatusServiceUnavailable {
		t.Fatalf("no loop: %d %s", res.Status, res.Raw)
	}
	plain := newAPIHarness(t)
	plain.svc.follower = func() FollowSyncer { return &fakeSyncer{err: follow.ErrNotFollowing} }
	res := plain.do(http.MethodPost, "/config/follow/sync", "", nil)
	if res.Status != http.StatusConflict || res.Body["code"] != CodeNotFollowing {
		t.Fatalf("not following: %d %s", res.Status, res.Raw)
	}
	if res := plain.do(http.MethodGet, "/config/follow", "", nil); res.Status != http.StatusOK || res.data()["following"] != false {
		t.Fatalf("GET on a non-follower: %d %s", res.Status, res.Raw)
	}
}

func TestLeadEntryWritesAreRefused(t *testing.T) {
	cases := []struct {
		name, method, path string
		body               any
		kind, entry        string
	}{
		{"edit template", http.MethodPut, "/config/templates/lt", map[string]any{"values": map[string]any{"model": "haiku"}}, "template", "lt"},
		{"delete template", http.MethodDelete, "/config/templates/lt", nil, "template", "lt"},
		{"rename template", http.MethodPost, "/config/templates/lt/rename", map[string]any{"new_name": "lt2"}, "template", "lt"},
		{"edit environment", http.MethodPut, "/config/environments/le", map[string]any{"values": map[string]any{"env": map[string]any{"A": "x"}}}, "environment", "le"},
		{"delete environment", http.MethodDelete, "/config/environments/le", nil, "environment", "le"},
		{"rename environment", http.MethodPost, "/config/environments/le/rename", map[string]any{"new_name": "le2"}, "environment", "le"},
		{"edit profile cell", http.MethodPut, "/config/delegation/profiles/day/roles/implement", map[string]any{"template": "mine"}, "profile", "day"},
		{"delete profile", http.MethodDelete, "/config/delegation/profiles/night", nil, "profile", "night"},
		{"rename profile", http.MethodPost, "/config/delegation/profiles/day/rename", map[string]any{"new_name": "d2"}, "profile", "day"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFollowerAPIHarness(t, nil)
			before := h.fileBytes()
			res := h.do(tc.method, tc.path, h.liveRevision(), tc.body)
			if res.Status != http.StatusConflict || res.Body["code"] != CodeOwnedByLead {
				t.Fatalf("%s %s = %d %s", tc.method, tc.path, res.Status, res.Raw)
			}
			d := res.data()
			if d["kind"] != tc.kind || d["name"] != tc.entry || d["lead"] != "lead" {
				t.Fatalf("data = %v", d)
			}
			if h.fileBytes() != before {
				t.Fatalf("leo.yaml changed:\n%s", h.fileBytes())
			}
		})
	}
}

func TestOverrideRoutes(t *testing.T) {
	cases := []struct {
		path, getPath string
		kind          config.EntryKind
		entry         string
	}{
		{"/config/templates/lt/override", "/config/templates/lt", config.KindTemplate, "lt"},
		{"/config/environments/le/override", "/config/environments/le", config.KindEnvironment, "le"},
		{"/config/delegation/profiles/day/override", "", config.KindProfile, "day"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			h := newFollowerAPIHarness(t, nil)
			res := h.do(http.MethodPost, tc.path, h.liveRevision(), nil)
			if res.Status != http.StatusOK {
				t.Fatalf("override = %d %s", res.Status, res.Raw)
			}
			if src := h.loadConfig().EntrySource(tc.kind, tc.entry); src != config.SourceLocalOverrideLead {
				t.Fatalf("source after override = %q", src)
			}
			if tc.getPath != "" {
				if got := h.do(http.MethodGet, tc.getPath, "", nil).data()["source"]; got != "local-override-lead" {
					t.Fatalf("GET source = %v", got)
				}
			}
			if res := h.do(http.MethodPost, tc.path, h.liveRevision(), nil); res.Status < 400 {
				t.Fatalf("second override = %d %s", res.Status, res.Raw)
			}
		})
	}
	h := newFollowerAPIHarness(t, nil)
	if res := h.do(http.MethodPost, "/config/templates/bad/override", h.liveRevision(), nil); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("override of an invalid lead entry = %d %s", res.Status, res.Raw)
	}
	if res := h.do(http.MethodPost, "/config/templates/nope/override", h.liveRevision(), nil); res.Status != http.StatusNotFound {
		t.Fatalf("override of an unknown entry = %d %s", res.Status, res.Raw)
	}
	if res := h.do(http.MethodPost, "/config/templates/lt/override", "", nil); res.Status != http.StatusPreconditionRequired {
		t.Fatalf("override without If-Match = %d", res.Status)
	}
}

func TestItemDocumentsCarrySourceAndInvalid(t *testing.T) {
	h := newFollowerAPIHarness(t, nil)
	items := map[string]map[string]any{}
	for _, it := range h.do(http.MethodGet, "/config/templates", "", nil).data()["items"].([]any) {
		m := it.(map[string]any)
		items[m["name"].(string)] = m
	}
	if items["lt"]["source"] != "lead" || items["mine"]["source"] != "local" {
		t.Fatalf("template sources = %v", items)
	}
	if items["bad"]["source"] != "lead" || items["bad"]["invalid"] == nil || items["lt"]["invalid"] != nil {
		t.Fatalf("invalid lead template = %v", items["bad"])
	}
	if d := h.do(http.MethodGet, "/config/templates/bad", "", nil).data(); d["invalid"] == nil {
		t.Fatalf("GET invalid template = %v", d)
	}
	if d := h.do(http.MethodGet, "/config/environments/le", "", nil).data(); d["source"] != "lead" {
		t.Fatalf("environment = %v", d)
	}
	var day map[string]any
	for _, p := range h.delegationDoc()["profiles"].([]any) {
		if m := p.(map[string]any); m["name"] == "day" {
			day = m
		}
	}
	if day["source"] != "lead" {
		t.Fatalf("profile = %v", day)
	}

	plain := newAPIHarness(t)
	for _, it := range plain.do(http.MethodGet, "/config/templates", "", nil).data()["items"].([]any) {
		if m := it.(map[string]any); m["source"] != nil || m["invalid"] != nil {
			t.Fatalf("non-follower item carries follow fields: %v", m)
		}
	}
}
