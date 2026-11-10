package web

import (
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

const leadRulesSections = `"templates":{},"environments":null,"delegation":null,"defaults":{},` +
	`"appearance_rules":[{"when":{"field":"name","op":"glob","value":"lead-*"},"color":"teal"}]`

const localRuleYAML = "appearance_rules:\n  - when: {field: name, op: is, value: mine}\n    color: red\n"

func followerRulesHarness(t *testing.T, sections, local string) *apiHarness {
	t.Helper()
	path := writeFollowerHome(t, sections, local)
	svc := NewConfigService(ConfigServiceOptions{ConfigPath: path, Reloader: &countingReloader{}})
	return newAPIHarnessOver(t, svc, path, "")
}

func TestAppearanceRulesGetSource(t *testing.T) {
	cases := []struct {
		name       string
		harness    func(t *testing.T) *apiHarness
		wantSource string
		wantRules  int
	}{
		{"plain host", func(t *testing.T) *apiHarness {
			svc, path := newConfigService(t, &countingReloader{})
			return newAPIHarnessOver(t, svc, path, "")
		}, config.AppearanceRulesSourceLocal, 0},
		{"follower without a local key", func(t *testing.T) *apiHarness {
			return followerRulesHarness(t, leadRulesSections, "")
		}, config.AppearanceRulesSourceLead, 1},
		{"follower with a local list", func(t *testing.T) *apiHarness {
			return followerRulesHarness(t, leadRulesSections, localRuleYAML)
		}, config.AppearanceRulesSourceLocal, 1},
		{"follower with a local empty list", func(t *testing.T) *apiHarness {
			return followerRulesHarness(t, leadRulesSections, "appearance_rules: []\n")
		}, config.AppearanceRulesSourceLocal, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.harness(t)
			res := h.do(http.MethodGet, "/appearance/rules", "", nil)
			wantStatus(t, res, http.StatusOK)
			d := res.data()
			if d["source"] != c.wantSource {
				t.Errorf("source = %v, want %s", d["source"], c.wantSource)
			}
			if rules, _ := d["rules"].([]any); len(rules) != c.wantRules {
				t.Errorf("rules = %v, want %d", d["rules"], c.wantRules)
			}
			if d["revision"] != h.liveRevision() {
				t.Errorf("revision = %v, want %s", d["revision"], h.liveRevision())
			}
		})
	}
}

func TestAppearanceRulesWireFollowerUsingLeadRules(t *testing.T) {
	h := followerRulesHarness(t, leadRulesSections, "")
	assertRulesWire(t, "appearance-rules-lead.json", h.do(http.MethodGet, "/appearance/rules", "", nil))
}

func TestAppearanceRulesPutOnFollowerCreatesOverride(t *testing.T) {
	h := followerRulesHarness(t, leadRulesSections, "")
	res := h.do(http.MethodPut, "/appearance/rules", h.liveRevision(), map[string]any{"rules": twoRules})
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["source"] != config.AppearanceRulesSourceLocal {
		t.Fatalf("source = %v, want local after a PUT", d["source"])
	}
	if rules, _ := d["rules"].([]any); len(rules) != 2 {
		t.Fatalf("rules = %v, want the two PUT rules", d["rules"])
	}
	if local, ok := h.loadConfig().LocalAppearanceRules(); !ok || len(local) != 2 {
		t.Fatalf("leo.yaml rules = %v (present %v), want the override", local, ok)
	}
}

func TestAppearanceRulesRevisionChangesOnLeadPull(t *testing.T) {
	h := followerRulesHarness(t, leadRulesSections, "")
	before := h.do(http.MethodGet, "/appearance/rules", "", nil).data()["revision"]

	changed := `"templates":{},"environments":null,"delegation":null,"defaults":{},` +
		`"appearance_rules":[{"when":{"field":"name","op":"glob","value":"lead-*"},"color":"blue"}]`
	export := `{"schema":2,"kind":"leo.follow.export","leo_version":"v0.80.0","home_id":"ab","revision":"sha256:leadrev2",` + changed + `}`
	exp, err := config.ParseFollowExport([]byte(export))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFollowCache(filepath.Join(filepath.Dir(h.path), "state"), config.FollowCache{
		Host: "lead", SSH: "evan@10.0.2.10", PulledAt: time.Now(), LeadRevision: exp.Revision, LeadVersion: exp.LeoVersion,
		Digest: exp.Digest(), Export: []byte(export),
	}); err != nil {
		t.Fatal(err)
	}

	after := h.do(http.MethodGet, "/appearance/rules", "", nil).data()
	if after["revision"] == before {
		t.Fatal("revision did not change after a pull changed the lead's rules")
	}
	rules, _ := after["rules"].([]any)
	if len(rules) != 1 || !reflect.DeepEqual(rules[0].(map[string]any)["color"], "blue") {
		t.Fatalf("rules after the pull = %v, want the lead's blue rule", after["rules"])
	}
	stale := h.do(http.MethodPut, "/appearance/rules", before.(string), map[string]any{"rules": twoRules})
	wantStatus(t, stale, http.StatusConflict)
}

func TestAppearanceRulesDelete(t *testing.T) {
	t.Run("a follower reverts to the lead's list", func(t *testing.T) {
		h := followerRulesHarness(t, leadRulesSections, localRuleYAML)
		res := h.do(http.MethodDelete, "/appearance/rules", h.liveRevision(), nil)
		wantStatus(t, res, http.StatusOK)
		d := res.data()
		if d["source"] != config.AppearanceRulesSourceLead {
			t.Fatalf("source = %v, want lead", d["source"])
		}
		if rules, _ := d["rules"].([]any); len(rules) != 1 || rules[0].(map[string]any)["color"] != "teal" {
			t.Fatalf("rules = %v, want the lead's teal rule", d["rules"])
		}
		if res.Header.Get("ETag") != `"`+h.liveRevision()+`"` || d["revision"] != h.liveRevision() {
			t.Fatalf("revision %v / ETag %q, want the new live revision %s", d["revision"], res.Header.Get("ETag"), h.liveRevision())
		}
		if _, ok := h.loadConfig().LocalAppearanceRules(); ok || hasKey(h.fileBytes(), "appearance_rules") {
			t.Fatalf("the local key survived:\n%s", h.fileBytes())
		}
	})

	t.Run("an empty local override is removed too", func(t *testing.T) {
		h := followerRulesHarness(t, leadRulesSections, "appearance_rules: []\n")
		res := h.do(http.MethodDelete, "/appearance/rules", h.liveRevision(), nil)
		wantStatus(t, res, http.StatusOK)
		if res.data()["source"] != config.AppearanceRulesSourceLead {
			t.Fatalf("source = %v, want lead", res.data()["source"])
		}
	})

	t.Run("a plain host ends with no rules", func(t *testing.T) {
		svc, path := newConfigService(t, &countingReloader{})
		h := newAPIHarnessOver(t, svc, path, "")
		wantStatus(t, h.do(http.MethodPut, "/appearance/rules", h.revision(), map[string]any{"rules": twoRules}), http.StatusOK)
		res := h.do(http.MethodDelete, "/appearance/rules", h.revision(), nil)
		wantStatus(t, res, http.StatusOK)
		d := res.data()
		if rules, _ := d["rules"].([]any); len(rules) != 0 || d["source"] != config.AppearanceRulesSourceLocal {
			t.Fatalf("document = %s, want no rules, source local", res.Raw)
		}
		if hasKey(h.fileBytes(), "appearance_rules") {
			t.Fatalf("the key survived:\n%s", h.fileBytes())
		}
	})

	t.Run("428 without If-Match and 409 when stale", func(t *testing.T) {
		h := followerRulesHarness(t, leadRulesSections, localRuleYAML)
		before := h.fileBytes()
		res := h.do(http.MethodDelete, "/appearance/rules", "", nil)
		wantStatus(t, res, http.StatusPreconditionRequired)
		wantCode(t, res, CodeRevisionRequired)
		res = h.do(http.MethodDelete, "/appearance/rules", "sha256:stale", nil)
		wantStatus(t, res, http.StatusConflict)
		wantCode(t, res, CodeRevisionMismatch)
		if h.fileBytes() != before {
			t.Fatal("a refused DELETE changed leo.yaml")
		}
	})

	t.Run("with no local key it is a 200 that leaves leo.yaml untouched", func(t *testing.T) {
		for name, h := range map[string]*apiHarness{
			"follower": followerRulesHarness(t, leadRulesSections, ""),
			"plain": func() *apiHarness {
				svc, path := newConfigService(t, &countingReloader{})
				return newAPIHarnessOver(t, svc, path, "")
			}(),
		} {
			before, rev := h.fileBytes(), h.liveRevision()
			res := h.do(http.MethodDelete, "/appearance/rules", rev, nil)
			wantStatus(t, res, http.StatusOK)
			if res.data()["revision"] != rev {
				t.Errorf("%s: revision = %v, want unchanged %s", name, res.data()["revision"], rev)
			}
			if h.fileBytes() != before {
				t.Errorf("%s: a no-op DELETE rewrote leo.yaml", name)
			}
		}
	})
}

// hasKey reports whether a YAML document has a top-level "<key>:" line.
func hasKey(text, key string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, key+":") {
			return true
		}
	}
	return false
}
