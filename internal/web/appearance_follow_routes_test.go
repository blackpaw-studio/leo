package web

import (
	"net/http"
	"path/filepath"
	"reflect"
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
