package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const leadLeoYAML = `templates_file: shared.yaml
defaults:
  model: opus
  max_turns: 7
  harness_options:
    bypass_permissions: true
    permission_mode: plan
  dispatch:
    max_concurrent: 3
  environments: [base]
web:
  enabled: true
  port: 9999
client:
  hosts:
    other: {ssh: evan@10.0.0.9}
api_clients:
  box: {can_message: [coder]}
chat:
  permission_timeout: 60s
tasks:
  nightly:
    schedule: "0 3 * * *"
    prompt_file: nightly.md
templates:
  coder:
    workspace: ~/work
    model: sonnet
    max_turns: 12
    add_dirs: [~/src]
environments:
  base:
    TOKEN: "${HOME}/tok"
delegation:
  enabled: true
  active_profile: day
  roles:
    implement: {use_for: write code}
  profiles:
    day:
      roles:
        implement: coder
        review: {template: reviewer, model: opus, effort: high}
`

const leadSharedYAML = `templates:
  reviewer:
    workspace: ~/review
    harness_options:
      permission_mode: plan
`

func writeLead(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "shared.yaml", leadSharedYAML)
	return writeFile(t, dir, "leo.yaml", leadLeoYAML)
}

func TestBuildFollowExportContent(t *testing.T) {
	path := writeLead(t)
	exp, err := BuildFollowExport(path, "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	_, rev, err := LoadWithRevision(path)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Schema != FollowExportSchema || exp.Kind != FollowExportKind || exp.LeoVersion != "v9.9.9" || exp.Revision != rev {
		t.Fatalf("header = %d %q %q %q, want schema/kind/v9.9.9/%s", exp.Schema, exp.Kind, exp.LeoVersion, exp.Revision, rev)
	}
	cfg, _ := Load(path)
	if id, _ := HomeID(cfg.StatePath()); exp.HomeID != id {
		t.Fatalf("home_id = %q, want %q", exp.HomeID, id)
	}
	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tasks", "web", "client", "api_clients", "chat", "templates_file", "follow"} {
		if _, ok := top[key]; ok {
			t.Errorf("export carries %q", key)
		}
	}
	text := string(data)
	for _, want := range []string{`"workspace":"~/work"`, `"~/src"`, `"workspace":"~/review"`, `"TOKEN":"${HOME}/tok"`} {
		if !strings.Contains(text, want) {
			t.Errorf("export lacks %s:\n%s", want, text)
		}
	}
	if _, ok := exp.Templates["reviewer"]; !ok {
		t.Errorf("export lacks the templates_file entry: %v", exp.Templates)
	}
}

func TestFollowExportRoundTripsTypes(t *testing.T) {
	path := writeLead(t)
	exp, err := BuildFollowExport(path, "v1")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(exp)
	parsed, err := ParseFollowExport(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsed.decodeSections()
	if err != nil {
		t.Fatal(err)
	}
	want, err := loadUnexpanded(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.templates, want.Templates) {
		t.Errorf("templates:\n got %#v\nwant %#v", got.templates, want.Templates)
	}
	if !reflect.DeepEqual(got.environments, want.Environments) {
		t.Errorf("environments:\n got %#v\nwant %#v", got.environments, want.Environments)
	}
	if !reflect.DeepEqual(got.delegation, want.Delegation) {
		t.Errorf("delegation:\n got %#v\nwant %#v", got.delegation, want.Delegation)
	}
	if !reflect.DeepEqual(got.defaults, want.Defaults) {
		t.Errorf("defaults:\n got %#v\nwant %#v", got.defaults, want.Defaults)
	}
	if v := got.defaults.HarnessOptions["bypass_permissions"]; v != true {
		t.Errorf("bool lost its type: %#v", v)
	}
	if n := got.defaults.Dispatch.MaxConcurrent; n == nil || *n != 3 {
		t.Errorf("max_concurrent = %v, want 3", n)
	}
	if r := got.delegation.Profiles["day"].Roles["implement"]; r != (RoleTarget{Template: "coder"}) {
		t.Errorf("shorthand role target = %#v", r)
	}
}

func TestBuildFollowExportRefusals(t *testing.T) {
	t.Run("lead is follower", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "leo.yaml", "client:\n  hosts:\n    up: {ssh: evan@10.0.0.1}\nfollow:\n  host: up\n")
		_, err := BuildFollowExport(path, "v1")
		var fe *FollowError
		if !errors.As(err, &fe) || fe.Code != FollowLeadIsFollower {
			t.Fatalf("err = %v, want %s", err, FollowLeadIsFollower)
		}
	})
	t.Run("lead config invalid", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "leo.yaml", "templates:\n  bad:\n    max_turns: -1\n")
		_, err := BuildFollowExport(path, "v1")
		var fe *FollowError
		if !errors.As(err, &fe) || fe.Code != FollowLeadConfigInvalid {
			t.Fatalf("err = %v, want %s", err, FollowLeadConfigInvalid)
		}
	})
}

func TestParseFollowExportRejects(t *testing.T) {
	cases := map[string]struct {
		body string
		code FollowErrorCode
	}{
		"not json":     {`nope`, FollowMalformed},
		"wrong kind":   {`{"schema":1,"kind":"other"}`, FollowMalformed},
		"newer schema": {`{"schema":2,"kind":"leo.follow.export"}`, FollowFollowerTooOld},
		"no schema":    {`{"kind":"leo.follow.export"}`, FollowMalformed},
		"bad section":  {`{"schema":1,"kind":"leo.follow.export","templates":{"x":{"max_turns":"lots"}}}`, FollowMalformed},
		"trailing":     {`{"schema":1,"kind":"leo.follow.export"} {}`, FollowMalformed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exp, err := ParseFollowExport([]byte(tc.body))
			if err == nil {
				_, err = exp.decodeSections()
			}
			var fe *FollowError
			if !errors.As(err, &fe) || fe.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestFollowExportDigestIsCanonical(t *testing.T) {
	a, err := ParseFollowExport([]byte(`{"schema":1,"kind":"leo.follow.export","revision":"r1","environments":{"e":{"B":"2","A":"1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFollowExport([]byte(`{"kind":"leo.follow.export","schema":1,"revision":"r2","environments":{"e":{"A":"1","B":"2"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() != b.Digest() {
		t.Fatalf("digest depends on key order or header: %s vs %s", a.Digest(), b.Digest())
	}
	if !bytes.HasPrefix([]byte(a.Digest()), []byte("sha256:")) {
		t.Fatalf("digest = %q", a.Digest())
	}
}
