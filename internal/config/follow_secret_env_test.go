package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const leadRefsYAML = `environments:
  le:
    OP_TOKEN: {secret: keychain, service: OP, account: evan}
    REGION: us
templates:
  lt:
    model: opus
    env:
      CREDS: {secret: systemd-creds, name: op-token}
`

func exportOf(t *testing.T, leadYAML string) (FollowExport, []byte) {
	t.Helper()
	exp, err := BuildFollowExport(writeFile(t, t.TempDir(), "leo.yaml", leadYAML), "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatal(err)
	}
	return exp, data
}

func TestFollowExportSchemaTracksSyncedRefs(t *testing.T) {
	cases := map[string]struct {
		yaml   string
		schema int
	}{
		"ref-free":             {"environments:\n  le: {A: \"1\"}\n", 1},
		"environment ref":      {"environments:\n  le: {A: {secret: file, path: /etc/a}}\n", 2},
		"template ref":         {"templates:\n  lt: {env: {A: {secret: file, path: /etc/a}}}\n", 2},
		"task ref is not sent": {"tasks:\n  t: {schedule: \"0 * * * *\", prompt_file: t.md, env: {A: {secret: file, path: /etc/a}}}\n", 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exp, data := exportOf(t, tc.yaml)
			if exp.Schema != tc.schema || !bytes.HasPrefix(data, []byte(`{"schema":`+string(rune('0'+tc.schema))+`,`)) {
				t.Fatalf("schema = %d, want %d:\n%s", exp.Schema, tc.schema, data)
			}
		})
	}
}

func TestFollowExportCarriesRefsNotValues(t *testing.T) {
	exp, data := exportOf(t, leadRefsYAML)
	for _, want := range []string{`"OP_TOKEN":{"secret":"keychain","service":"OP","account":"evan"}`, `"secret":"systemd-creds"`, `"name":"op-token"`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("export lacks %s:\n%s", want, data)
		}
	}
	parsed, sections, err := parseFollowExport(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Digest() != exp.Digest() {
		t.Errorf("digest changed across the round trip")
	}
	wantOP := secretenv.Ref{Backend: secretenv.Keychain, Service: "OP", Account: "evan"}
	if r, ok := sections.environments["le"]["OP_TOKEN"].Ref(); !ok || !reflect.DeepEqual(r, wantOP) {
		t.Errorf("environment ref = %+v, %v", r, ok)
	}
	if r, ok := sections.templates["lt"].Env["CREDS"].Ref(); !ok || r.Backend != secretenv.SystemdCreds || r.Name != "op-token" {
		t.Errorf("template ref = %+v, %v", r, ok)
	}
}

func TestFollowerCachesAndAppliesLeadRefs(t *testing.T) {
	allowProbes(t)
	dir := t.TempDir()
	writeLeadCache(t, dir, leadRefsYAML)
	matches, _ := filepath.Glob(filepath.Join(dir, "state", "*"))
	var cached []byte
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil && bytes.Contains(b, []byte("leo.follow.export")) {
			cached = b
		}
	}
	if !bytes.Contains(cached, []byte(`\"secret\":\"keychain\"`)) && !bytes.Contains(cached, []byte(`"secret":"keychain"`)) {
		t.Fatalf("follow cache lacks the ref:\n%s", cached)
	}

	// Platform-specific backends stay valid on any follower: validation
	// is shape-only and a platform mismatch fails only at resolution.
	cfg := mustLoad(t, writeFile(t, dir, "leo.yaml", followerHeader))
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if inv := cfg.InvalidEntries(KindEnvironment); len(inv) != 0 {
		t.Errorf("invalid environments: %v", inv)
	}
	if inv := cfg.InvalidEntries(KindTemplate); len(inv) != 0 {
		t.Errorf("invalid templates: %v", inv)
	}
	if _, ok := cfg.Environments["le"]["OP_TOKEN"].Ref(); !ok {
		t.Errorf("lead environment ref not applied: %v", cfg.Environments["le"])
	}
	if _, ok := cfg.Templates["lt"].Env["CREDS"].Ref(); !ok {
		t.Errorf("lead template ref not applied: %v", cfg.Templates["lt"].Env)
	}
}

func TestLeadEntryWithBadRefIsIsolated(t *testing.T) {
	allowProbes(t)
	dir := t.TempDir()
	writeRawLeadCacheSchema(t, dir, 2, `"environments":{"bad":{"OP":{"secret":"vault"}},"good":{"A":"1"}},`+
		`"templates":{"tbad":{"env":{"OP":{"secret":"keychain"}}},"tgood":{"env":{"OP":{"secret":"file","path":"/etc/op"}}}}`)
	cfg, err := Load(writeFile(t, dir, "leo.yaml", followerHeader))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed on isolated lead entries: %v", err)
	}
	_, err = cfg.LookupEnvironment("bad")
	if inv := invalidReason(t, err); !strings.Contains(inv.Reason, "vault") {
		t.Errorf("environment reason = %q", inv.Reason)
	}
	_, err = cfg.LookupTemplate("tbad")
	if inv := invalidReason(t, err); !strings.Contains(inv.Reason, "OP") {
		t.Errorf("template reason = %q", inv.Reason)
	}
	if _, err := cfg.LookupEnvironment("good"); err != nil {
		t.Errorf("good environment: %v", err)
	}
	if _, err := cfg.LookupTemplate("tgood"); err != nil {
		t.Errorf("good template: %v", err)
	}
}
