package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

type countingReloader struct{ calls int }

func (r *countingReloader) ReloadConfig() error { r.calls++; return nil }

var twoRules = []map[string]any{
	{"when": map[string]any{"field": "environments", "op": "contains", "value": "prod"}, "color": "red"},
	{"when": map[string]any{"field": "name", "op": "glob", "value": "alfred-*"}, "color": "pink", "emoji": "🎩"},
}

// assertRulesWire compares a rules response, revision masked, with the
// observe package's golden wire fixture.
func assertRulesWire(t *testing.T, fixture string, res apiResult) {
	t.Helper()
	assertWireFixture(t, fixture, res.Status, res.Raw)
}

// assertWireFixture compares a response with a {status, body} golden from
// the observe package's wire fixtures, which LeoKit decodes too. A revision
// in the body is masked.
func assertWireFixture(t *testing.T, fixture string, status int, raw []byte) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if d, ok := body["data"].(map[string]any); ok {
		if _, has := d["revision"]; has {
			d["revision"] = "sha256:" + strings.Repeat("0", 64)
		}
	}
	golden, err := os.ReadFile("../observe/testdata/wire/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{"status": float64(status), "body": body}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("%s mismatch\n--- want\n%s\n--- got\n%s", fixture, golden, gotJSON)
	}
}

func TestAppearanceRulesRoutes(t *testing.T) {
	reload := &countingReloader{}
	svc, path := newConfigService(t, reload)
	h := newAPIHarnessOver(t, svc, path, "")

	res := h.do(http.MethodGet, "/appearance/rules", "", nil)
	wantStatus(t, res, http.StatusOK)
	if d := res.data(); d["revision"] != h.revision() || !reflect.DeepEqual(d["rules"], []any{}) || !reflect.DeepEqual(d["errors"], []any{}) {
		t.Fatalf("empty rules document = %s", res.Raw)
	}
	if res.Header.Get("ETag") != `"`+h.revision()+`"` {
		t.Fatalf("ETag = %q", res.Header.Get("ETag"))
	}

	t.Run("428 without If-Match", func(t *testing.T) {
		res := h.do(http.MethodPut, "/appearance/rules", "", map[string]any{"rules": twoRules})
		wantStatus(t, res, http.StatusPreconditionRequired)
		wantCode(t, res, CodeRevisionRequired)
	})
	t.Run("409 on a stale revision", func(t *testing.T) {
		res := h.do(http.MethodPut, "/appearance/rules", "sha256:stale", map[string]any{"rules": twoRules})
		wantStatus(t, res, http.StatusConflict)
		wantCode(t, res, CodeRevisionMismatch)
	})
	t.Run("400 names the bad rule and why", func(t *testing.T) {
		before := h.fileBytes()
		bad := []map[string]any{twoRules[0], {"when": map[string]any{"field": "name", "op": "is", "value": "x"}, "color": "magenta"}}
		res := h.do(http.MethodPut, "/appearance/rules", h.revision(), map[string]any{"rules": bad})
		assertRulesWire(t, "appearance-rule-error.json", res)
		if h.fileBytes() != before {
			t.Fatal("a rejected rules PUT changed leo.yaml")
		}
	})
	t.Run("400 on a malformed body", func(t *testing.T) {
		res := h.do(http.MethodPut, "/appearance/rules", h.revision(), `{"rules": [{"when": "oops"}]}`)
		wantStatus(t, res, http.StatusBadRequest)
		wantCode(t, res, CodeBadRequest)
		res = h.do(http.MethodPut, "/appearance/rules", h.revision(), `{}`)
		wantStatus(t, res, http.StatusBadRequest)
	})

	res = h.do(http.MethodPut, "/appearance/rules", h.revision(), map[string]any{"rules": twoRules})
	wantStatus(t, res, http.StatusOK)
	if res.data()["revision"] != h.revision() || res.Header.Get("ETag") != `"`+h.revision()+`"` {
		t.Fatalf("PUT revision = %v / %q, want the new on-disk %s", res.data()["revision"], res.Header.Get("ETag"), h.revision())
	}
	if reload.calls != 1 {
		t.Fatalf("reloads = %d, want 1 (the reload republishes appearance)", reload.calls)
	}
	cfg := h.loadConfig()
	if len(cfg.AppearanceRules) != 2 || cfg.AppearanceRules[1].Emoji != "🎩" || cfg.AppearanceRules[0].When.Value != "prod" {
		t.Fatalf("persisted rules = %+v", cfg.AppearanceRules)
	}

	// A rule hand-edited invalid on disk is listed with its error.
	cfg.AppearanceRules = append(cfg.AppearanceRules, config.AppearanceRule{
		When: config.AppearanceCondition{Field: "template", Op: "is", Value: "coder"}, Color: "magenta",
	})
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	assertRulesWire(t, "appearance-rules.json", h.do(http.MethodGet, "/appearance/rules", "", nil))

	// An empty list clears the rules.
	res = h.do(http.MethodPut, "/appearance/rules", h.revision(), map[string]any{"rules": []any{}})
	wantStatus(t, res, http.StatusOK)
	if got := h.loadConfig().AppearanceRules; len(got) != 0 || strings.Contains(h.fileBytes(), "appearance_rules") {
		t.Fatalf("rules after clearing = %+v", got)
	}
}

// A rule hand-edited invalid on disk is a warning: it must not block config
// API writes to unrelated keys.
func TestConfigWriteSucceedsWithBadAppearanceRuleOnDisk(t *testing.T) {
	svc, path := newConfigService(t, &countingReloader{})
	h := newAPIHarnessOver(t, svc, path, "")
	cfg := h.loadConfig()
	cfg.AppearanceRules = config.AppearanceRuleList{{
		When: config.AppearanceCondition{Field: "name", Op: "is", Value: "x"}, Color: "magenta",
	}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	res := h.do(http.MethodPut, "/config/defaults", h.revision(), map[string]any{"values": map[string]any{"model": "opus"}})
	wantStatus(t, res, http.StatusOK)
	if got := h.loadConfig().AppearanceRules; len(got) != 1 || got[0].Color != "magenta" {
		t.Fatalf("rules after an unrelated write = %+v, want the bad rule kept", got)
	}
}

func TestAppearanceRulesRoutesAreOperatorOnly(t *testing.T) {
	s := newTokenSplitServer(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/appearance/rules"},
		{http.MethodPut, "/api/v1/appearance/rules"},
		{http.MethodPut, "/api/v1/agents/x/appearance"},
	} {
		if w := requestAs(t, s, route.method, route.path, testAgentToken, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with the agent token = %d, want 403", route.method, route.path, w.Code)
		}
	}
}

// fakeAppearanceService is the agent core behind PUT /agents/{name}/appearance.
type fakeAppearanceService struct {
	records   map[string]agent.Record
	lastName  string
	lastPatch agentstore.AppearancePatch
	result    *observe.Appearance
	err       error
}

func (f *fakeAppearanceService) Resolve(query string) (agent.Record, error) {
	if rec, ok := f.records[query]; ok {
		return rec, nil
	}
	return agent.Record{}, &agent.ErrNotFound{Query: query}
}

func (f *fakeAppearanceService) SetAppearance(name string, patch agentstore.AppearancePatch) (*observe.Appearance, error) {
	f.lastName, f.lastPatch = name, patch
	return f.result, f.err
}

func putAppearance(t *testing.T, svc AppearanceService, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /agents/{name}/appearance", AgentAppearanceHandler(func() AppearanceService { return svc }))
	req := httptest.NewRequest(http.MethodPut, "/agents/"+name+"/appearance", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAgentAppearanceRoute(t *testing.T) {
	svc := &fakeAppearanceService{
		records: map[string]agent.Record{"olympus": {Name: "olympus"}},
		result:  &observe.Appearance{Emoji: "🏛️", EmojiSource: "manual", Color: "teal", ColorSource: "rule:0"},
	}

	w := putAppearance(t, svc, "olympus", `{"emoji": "🏛️", "color": null}`)
	assertWireFixture(t, "appearance-agent-put.json", w.Code, w.Body.Bytes())
	if svc.lastName != "olympus" || svc.lastPatch.Emoji == nil || *svc.lastPatch.Emoji != "🏛️" || svc.lastPatch.Color == nil || *svc.lastPatch.Color != "" {
		t.Fatalf("patch = %+v (emoji %v, color %v), want emoji set and color cleared", svc.lastPatch, svc.lastPatch.Emoji, svc.lastPatch.Color)
	}

	t.Run("absent keys are left unchanged", func(t *testing.T) {
		if w := putAppearance(t, svc, "olympus", `{}`); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if svc.lastPatch.Emoji != nil || svc.lastPatch.Color != nil {
			t.Fatalf("patch = %+v, want both unchanged", svc.lastPatch)
		}
	})
	t.Run("nothing set answers appearance null", func(t *testing.T) {
		svc.result = nil
		w := putAppearance(t, svc, "olympus", `{"emoji": null, "color": null}`)
		assertWireFixture(t, "appearance-agent-put-null.json", w.Code, w.Body.Bytes())
	})
	t.Run("404 for an unknown agent", func(t *testing.T) {
		w := putAppearance(t, svc, "ghost", `{"color": "red"}`)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Fatalf("status = %d body %s", w.Code, w.Body)
		}
	})
	t.Run("400 invalid_appearance carries the reason", func(t *testing.T) {
		svc.err = &agent.InvalidAppearanceError{Field: "color", Reason: `color "magenta" is not in the palette`}
		defer func() { svc.err = nil }()
		w := putAppearance(t, svc, "olympus", `{"color": "magenta"}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_appearance"`) || !strings.Contains(w.Body.String(), "magenta") {
			t.Fatalf("status = %d body %s", w.Code, w.Body)
		}
	})
	t.Run("400 on a malformed body", func(t *testing.T) {
		for _, body := range []string{`{"color": 3}`, `{"colour": "red"}`, `not json`} {
			if w := putAppearance(t, svc, "olympus", body); w.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", body, w.Code)
			}
		}
	})
	t.Run("503 without an agent service", func(t *testing.T) {
		if w := putAppearance(t, nil, "olympus", `{}`); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("an unexpected failure is a 500", func(t *testing.T) {
		svc.err = errors.New("disk full")
		defer func() { svc.err = nil }()
		if w := putAppearance(t, svc, "olympus", `{"color": "red"}`); w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d", w.Code)
		}
	})
}
