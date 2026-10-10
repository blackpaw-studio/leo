package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

const v1EnvironmentsConfig = `
defaults:
  model: sonnet
  environments: [base]
environments:
  work:
    CLAUDE_CONFIG_DIR: /acct/work
    SECRET_TOKEN: do-not-leak
  base:
    FOO: bar
templates:
  coding:
    model: sonnet
    environments: [base, work]
  research:
    model: opus
`

func newV1EnvironmentsServer(t *testing.T) (*Server, *mockAgentService) {
	t.Helper()
	s, dir, svc := newTestServerWithAgents(t)
	if err := os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(v1EnvironmentsConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, svc
}

func decodeData(t *testing.T, w interface{ Bytes() []byte }, into any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, into); err != nil {
		t.Fatalf("decoding %s: %v", env.Data, err)
	}
}

func TestBuildSnapshotAgentCarriesEffectiveEnvironments(t *testing.T) {
	cfg := &config.Config{
		Defaults:     config.DefaultsConfig{Environments: []string{"base"}},
		Environments: map[string]map[string]string{"base": {"A": "1"}, "work": {"B": "2"}},
		Templates: map[string]config.TemplateConfig{
			"coding":   {Environments: []string{"base", "work"}},
			"research": {},
		},
	}
	snap := buildSnapshot(snapshotInput{
		Config: cfg,
		Records: []agent.Record{
			{Name: "a-default", Template: "coding", Status: "running"},
			{Name: "b-override", Template: "coding", Status: "running", Environments: []string{"work"}},
			{Name: "c-defaults", Template: "research", Status: "running"},
			{Name: "d-broken", Template: "coding", Status: "running", Environments: []string{"gone"}},
		},
		Now: time.Now(),
	})
	type row struct {
		envs   []string
		source string
		err    *string
	}
	got := map[string]row{}
	for _, a := range snap.Agents {
		got[a.Name] = row{a.Environments, a.EnvironmentsSource, a.EnvironmentError}
	}
	for name, want := range map[string]row{
		"a-default":  {[]string{"base", "work"}, "default", nil},
		"b-override": {[]string{"work"}, "override", nil},
		"c-defaults": {[]string{"base"}, "default", nil},
	} {
		if !reflect.DeepEqual(got[name], want) {
			t.Errorf("%s = %+v, want %+v", name, got[name], want)
		}
	}
	if b := got["d-broken"]; b.err == nil || !strings.Contains(*b.err, "gone") || b.source != "override" {
		t.Errorf("d-broken = %+v, want an environment_error naming gone", b)
	}

	raw, _ := json.Marshal(snap.Agents[0])
	if !strings.Contains(string(raw), `"environment_error":null`) {
		t.Errorf("a healthy agent must report environment_error:null, got %s", raw)
	}
}

func TestV1EnvironmentsListsNamesOnlyInStableOrder(t *testing.T) {
	s, _ := newV1EnvironmentsServer(t)
	w := getRequest(t, s, "/api/v1/environments")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	decodeData(t, w.Body, &got)
	want := []map[string]any{{"name": "base"}, {"name": "work"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environments = %v, want %v (name only)", got, want)
	}
	if strings.Contains(w.Body.String(), "do-not-leak") || strings.Contains(w.Body.String(), "/acct/work") {
		t.Fatalf("environment values leaked: %s", w.Body.String())
	}
}

func TestV1TemplatesCarryDefaultEnvironments(t *testing.T) {
	s, _ := newV1EnvironmentsServer(t)
	w := getRequest(t, s, "/api/v1/templates")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got []observe.Template
	decodeData(t, w.Body, &got)
	if len(got) != 2 || got[0].Name != "coding" || !reflect.DeepEqual(got[0].Environments, []string{"base", "work"}) {
		t.Fatalf("templates = %+v", got)
	}
	if got[1].Name != "research" || got[1].Environments == nil || len(got[1].Environments) != 0 {
		t.Fatalf("a template with no list must report [], got %+v", got[1])
	}
	if strings.Contains(w.Body.String(), "OP_SERVICE_ACCOUNT_TOKEN") || strings.Contains(w.Body.String(), testSecretEnvValue) {
		t.Fatalf("template env leaked: %s", w.Body.String())
	}
}

func TestV1SpawnAndSetEnvironmentsRoutes(t *testing.T) {
	s, svc := newV1EnvironmentsServer(t)
	svc.records = []agent.Record{{Name: "leo-coding-leo", Template: "coding", Status: "running"}}

	w := postJSON(t, s, "/api/v1/agents/spawn", `{"template":"coding","environments":["work"]}`)
	if w.Code != http.StatusOK || !reflect.DeepEqual(svc.spawnSpec.Environments, []string{"work"}) {
		t.Fatalf("spawn: %d %s spec=%+v", w.Code, w.Body.String(), svc.spawnSpec)
	}

	w = postJSON(t, s, "/api/v1/agents/leo-coding-leo/environments", `{"environments":[]}`)
	if w.Code != http.StatusOK || !svc.setEnvCalled || svc.setEnvName != "leo-coding-leo" || len(svc.setEnvNames) != 0 {
		t.Fatalf("set (clear): %d %s called=%v names=%v", w.Code, w.Body.String(), svc.setEnvCalled, svc.setEnvNames)
	}
}

func TestV1EnvironmentErrorsCarryTypedCodes(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unknown", &config.UnknownEnvironmentError{Name: "nope"}, http.StatusBadRequest, "unknown_environment"},
		{"persistent task", &agent.PersistentTaskError{Agent: "leo-coding-leo", Task: "nightly"}, http.StatusConflict, "persistent_task"},
		{"harness", &agent.HarnessMismatchError{Agent: "leo-coding-leo", Template: "coding", Have: "claude", Want: "codex"}, http.StatusConflict, "harness_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, svc := newV1EnvironmentsServer(t)
			svc.records = []agent.Record{{Name: "leo-coding-leo", Template: "coding", Status: "running"}}
			svc.setEnvErr = tt.err
			w := postJSON(t, s, "/api/v1/agents/leo-coding-leo/environments", `{"environments":["work"]}`)
			var body struct{ Error, Code string }
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != tt.status || body.Code != tt.code || body.Error == "" {
				t.Fatalf("got %d %+v, want %d code %q", w.Code, body, tt.status, tt.code)
			}
		})
	}

	// A wrapped typed error still classifies: the code comes from errors.As.
	s, svc := newV1EnvironmentsServer(t)
	svc.spawnErr = &config.UnknownEnvironmentError{Name: "nope"}
	w := postJSON(t, s, "/api/v1/agents/spawn", `{"template":"coding","environments":["nope"]}`)
	if !strings.Contains(w.Body.String(), `"code":"unknown_environment"`) || w.Code != http.StatusBadRequest {
		t.Fatalf("spawn error: %d %s", w.Code, w.Body.String())
	}
}

func TestV1EnvironmentWritesAreOperatorOnly(t *testing.T) {
	s := newTokenSplitServer(t)
	for _, path := range []string{"/api/v1/agents/spawn", "/api/v1/agents/assistant/environments"} {
		if w := requestAs(t, s, "POST", path, testAgentToken, `{"environments":[]}`); w.Code != http.StatusForbidden {
			t.Errorf("POST %s with the agent token = %d, want 403", path, w.Code)
		}
		if w := requestAs(t, s, "POST", path, "", `{"environments":[]}`); w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a token = %d, want 401", path, w.Code)
		}
	}
}
