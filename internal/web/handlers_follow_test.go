package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/follow"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func newFollowerWebServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := writeFollowerHome(t, followerLeadSections, "templates:\n  mine: {model: haiku}\n")
	s := New(path, &mockProcesses{states: map[string]ProcessStateInfo{}},
		&mockScheduler{entries: []cron.EntryInfo{}}, &mockReloader{}, nil,
		Options{Port: testPort, APIToken: testAPIToken})
	raw := s.httpServer.Handler
	s.httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizeTestRequest(r)
		raw.ServeHTTP(w, r)
	})
	return s, path
}

func getPage(t *testing.T, s *Server, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestFollowerPagesShowSourcesAndOverride(t *testing.T) {
	s, _ := newFollowerWebServer(t)
	page := getPage(t, s, "/config/templates")
	for _, want := range []string{"lead · invalid", `hx-post="/web/template/lt/override"`, ">local<"} {
		if !strings.Contains(page, want) {
			t.Errorf("templates page lacks %q", want)
		}
	}
	edit := getPage(t, s, "/config/templates/lt")
	if !strings.Contains(edit, "<fieldset class=\"form-fieldset\" disabled>") || !strings.Contains(edit, "Override locally") || strings.Contains(edit, "/web/template/lt/rename") {
		t.Errorf("lead template edit page is not read-only with an override button")
	}
	if mine := getPage(t, s, "/config/templates/mine"); strings.Contains(mine, "Override locally") || strings.Contains(mine, " disabled>") {
		t.Errorf("local template edit page is read-only")
	}
	envs := getPage(t, s, "/config/environments")
	if !strings.Contains(envs, `hx-post="/web/environment/le/override"`) {
		t.Errorf("environments page lacks the override button")
	}
	deleg := getPage(t, s, "/config/delegation")
	if !strings.Contains(deleg, `hx-post="/web/delegation/profile/override"`) || !strings.Contains(deleg, ">lead<") {
		t.Errorf("delegation page lacks the profile badge or override")
	}
}

func TestFollowerHTMLWritesAreRefused(t *testing.T) {
	s, path := newFollowerWebServer(t)
	before := readFile(t, path)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/web/template/lt", nil))
	if !strings.Contains(w.Body.String(), "owned by lead") {
		t.Fatalf("delete flash = %s", w.Body.String())
	}
	postForm(t, s, "/web/delegation/profile/delete", url.Values{"name": {"night"}})
	if readFile(t, path) != before {
		t.Fatal("leo.yaml changed")
	}
}

func TestFollowerHTMLOverride(t *testing.T) {
	cases := []struct {
		path     string
		form     url.Values
		kind     config.EntryKind
		name     string
		redirect string
	}{
		{"/web/template/lt/override", nil, config.KindTemplate, "lt", "/config/templates/lt"},
		{"/web/environment/le/override", nil, config.KindEnvironment, "le", "/config/environments"},
		{"/web/delegation/profile/override", url.Values{"name": {"night"}}, config.KindProfile, "night", "/config/delegation"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			s, path := newFollowerWebServer(t)
			w := postForm(t, s, tc.path, tc.form)
			if got := w.Header().Get("HX-Redirect"); got != tc.redirect {
				t.Fatalf("HX-Redirect = %q (%d %s)", got, w.Code, w.Body.String())
			}
			if src := loadConfigFile(t, path).EntrySource(tc.kind, tc.name); src != config.SourceLocalOverrideLead {
				t.Fatalf("source = %q", src)
			}
		})
	}
}

func TestSettingsFollowCardAndSyncNow(t *testing.T) {
	s, _ := newFollowerWebServer(t)
	page := getPage(t, s, "/config/settings")
	for _, want := range []string{"Follow lead", "sha256:leadrev", `hx-post="/web/follow/sync"`} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}

	syncer := &fakeSyncer{out: follow.Outcome{Changed: true}}
	s.ConfigService.follower = func() FollowSyncer { return syncer }
	w := postForm(t, s, "/web/follow/sync", nil)
	if syncer.calls != 1 || w.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("sync now = %d %s (calls %d)", w.Code, w.Body.String(), syncer.calls)
	}
	syncer.err = &config.FollowError{Code: config.FollowUnreachable, Detail: "refused"}
	if w := postForm(t, s, "/web/follow/sync", nil); !strings.Contains(w.Body.String(), "unreachable: refused") {
		t.Fatalf("failed sync flash = %s", w.Body.String())
	}

	plain, _ := newTestServer(t)
	if strings.Contains(getPage(t, plain, "/config/settings"), "Follow lead") {
		t.Error("non-follower settings page shows the follow card")
	}
}

func TestV1FollowerSourcesAndFollowRoute(t *testing.T) {
	s, _ := newFollowerWebServer(t)
	var templates []observe.Template
	decodeData(t, getRequest(t, s, "/api/v1/templates").Body, &templates)
	got := map[string]observe.Template{}
	for _, tmpl := range templates {
		got[tmpl.Name] = tmpl
	}
	if got["lt"].Source != "lead" || got["mine"].Source != "local" || got["bad"].Invalid == "" || got["bad"].Environments == nil {
		t.Fatalf("templates = %+v", got)
	}
	var envs []observe.Environment
	decodeData(t, getRequest(t, s, "/api/v1/environments").Body, &envs)
	if len(envs) != 1 || envs[0].Name != "le" || envs[0].Source != "lead" {
		t.Fatalf("environments = %+v", envs)
	}
	var report follow.Report
	w := getRequest(t, s, "/api/v1/follow")
	decodeData(t, w.Body, &report)
	if !report.Following || report.Host != "lead" {
		t.Fatalf("follow = %d %+v", w.Code, report)
	}

	plain, _ := newV1EnvironmentsServer(t)
	if body := getRequest(t, plain, "/api/v1/templates").Body.String(); strings.Contains(body, `"source"`) || strings.Contains(body, `"invalid"`) {
		t.Fatalf("non-follower templates carry follow fields: %s", body)
	}
}

func TestClassifyInvalidLeadEntry(t *testing.T) {
	err := fmt.Errorf("spawning agent: %w", &config.InvalidEntryError{Kind: config.KindTemplate, Name: "bad", Lead: "lead", Reason: "no binary"})
	if status, code := ClassifyEnvironmentError(err); status != http.StatusBadRequest || code != "invalid_lead_entry" {
		t.Fatalf("classify = %d %q", status, code)
	}
}
