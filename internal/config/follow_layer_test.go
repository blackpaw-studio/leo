package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// followerHeader makes a leo.yaml follow the host "lead".
const followerHeader = "client:\n  default_host: localhost\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\nfollow:\n  host: lead\n"

// writeLeadCache builds the export of a lead whose leo.yaml is leadYAML and
// stores it as the follower's cache under followerDir/state.
func writeLeadCache(t testing.TB, followerDir, leadYAML string) FollowExport {
	t.Helper()
	leadDir := t.TempDir()
	leadPath := writeFile(t, leadDir, "leo.yaml", leadYAML)
	exp, err := BuildFollowExport(leadPath, "v0.64.0")
	if err != nil {
		t.Fatalf("lead export: %v", err)
	}
	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseFollowExport(data)
	if err != nil {
		t.Fatal(err)
	}
	err = WriteFollowCache(filepath.Join(followerDir, "state"), FollowCache{
		Host: "lead", SSH: "evan@10.0.2.10", PulledAt: time.Now(),
		LeadRevision: exp.Revision, LeadVersion: exp.LeoVersion, LeadHomeID: exp.HomeID,
		Digest: parsed.Digest(), Export: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// loadFollower writes a follower leo.yaml (followerHeader + local) next to a
// cache of leadYAML and loads it.
func loadFollower(t *testing.T, leadYAML, local string) *Config {
	t.Helper()
	allowProbes(t)
	dir := t.TempDir()
	writeLeadCache(t, dir, leadYAML)
	path := writeFile(t, dir, "leo.yaml", followerHeader+local)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load follower: %v", err)
	}
	return cfg
}

func TestFollowLayerTemplatesReplaceByName(t *testing.T) {
	lead := "templates:\n  a: {model: opus, max_turns: 5}\n  b: {model: opus, workspace: ~/lead-ws}\n"
	cfg := loadFollower(t, lead, "templates:\n  a: {model: haiku}\n")
	if got := cfg.Templates["a"]; !reflect.DeepEqual(got, TemplateConfig{Model: "haiku"}) {
		t.Fatalf("a = %#v, want the local entry whole (no field merge)", got)
	}
	home, _ := os.UserHomeDir()
	if got := cfg.Templates["b"]; got.Model != "opus" || got.Workspace != filepath.Join(home, "lead-ws") {
		t.Fatalf("b = %#v, want the lead entry with ~ expanded locally", got)
	}
	if s := cfg.TemplateSource("a"); s != SourceLocalOverrideLead {
		t.Errorf("source(a) = %q", s)
	}
	if s := cfg.TemplateSource("b"); s != SourceLead {
		t.Errorf("source(b) = %q", s)
	}
}

func TestFollowLayerEnvironmentsReplaceByName(t *testing.T) {
	lead := "environments:\n  work: {A: lead, B: lead}\n  home: {H: lead}\n"
	cfg := loadFollower(t, lead, "environments:\n  work: {A: local}\n  mine: {M: local}\n")
	want := map[string]map[string]string{"work": {"A": "local"}, "home": {"H": "lead"}, "mine": {"M": "local"}}
	if !reflect.DeepEqual(cfg.Environments, want) {
		t.Fatalf("environments = %v, want %v", cfg.Environments, want)
	}
	for name, src := range map[string]TemplateSource{"work": SourceLocalOverrideLead, "home": SourceLead, "mine": SourceLocal} {
		if got := cfg.EntrySource(KindEnvironment, name); got != src {
			t.Errorf("source(%s) = %q, want %q", name, got, src)
		}
	}
}

func TestFollowLayerProfilesReplaceByName(t *testing.T) {
	lead := "templates:\n  t1: {}\n  t2: {}\ndelegation:\n  active_profile: day\n  profiles:\n    day: {roles: {implement: t1, review: t2}}\n    night: {roles: {implement: t2}}\n"
	cfg := loadFollower(t, lead, "delegation:\n  profiles:\n    day: {roles: {implement: t2}}\n")
	d := cfg.Delegation
	if d == nil || d.ActiveProfile != "day" {
		t.Fatalf("delegation = %#v", d)
	}
	if got := d.Profiles["day"].Roles; !reflect.DeepEqual(got, map[string]RoleTarget{"implement": {Template: "t2"}}) {
		t.Fatalf("day roles = %v, want the local profile whole", got)
	}
	if _, ok := d.Profiles["night"]; !ok {
		t.Fatal("lead profile night missing")
	}
	if s := cfg.EntrySource(KindProfile, "day"); s != SourceLocalOverrideLead {
		t.Errorf("source(day) = %q", s)
	}
	if s := cfg.EntrySource(KindProfile, "night"); s != SourceLead {
		t.Errorf("source(night) = %q", s)
	}
}

func TestFollowLayerSharedSitsBetweenLeadAndLocal(t *testing.T) {
	allowProbes(t)
	lead := "templates:\n  x: {model: opus}\n  y: {model: opus}\n  z: {model: opus}\n  w: {model: opus}\n"
	dir := t.TempDir()
	writeLeadCache(t, dir, lead)
	writeFile(t, dir, "shared.yaml", "templates:\n  y: {model: sonnet}\n  z: {model: sonnet}\n  v: {model: sonnet}\n")
	path := writeFile(t, dir, "leo.yaml", followerHeader+"templates_file: shared.yaml\ntemplates:\n  z: {model: haiku}\n  w: {model: haiku}\n  u: {model: haiku}\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		model  string
		source TemplateSource
	}{
		"x": {"opus", SourceLead},
		"y": {"sonnet", SourceSharedOverrideLead},
		"z": {"haiku", SourceLocalOverride},
		"w": {"haiku", SourceLocalOverrideLead},
		"v": {"sonnet", SourceShared},
		"u": {"haiku", SourceLocal},
	}
	if len(cfg.Templates) != len(want) {
		t.Fatalf("templates = %v", cfg.Templates)
	}
	for name, w := range want {
		if got := cfg.Templates[name].Model; got != w.model {
			t.Errorf("%s.model = %q, want %q", name, got, w.model)
		}
		if got := cfg.TemplateSource(name); got != w.source {
			t.Errorf("source(%s) = %q, want %q", name, got, w.source)
		}
	}
	if got := SourceLocalOverride.Label(); got != "local · overrides shared" {
		t.Errorf("z label = %q", got)
	}
}

func TestFollowLayerDefaultsKeys(t *testing.T) {
	lead := `defaults:
  model: opus
  max_turns: 9
  harness_options: {bypass_permissions: true, permission_mode: plan}
  dispatch: {max_concurrent: 4, approval_timeout: 10m}
  push: {finished: false}
`
	local := `defaults:
  model: haiku
  max_turns: 0
  harness_options: {permission_mode: default}
  dispatch: {max_concurrent: 2}
`
	cfg := loadFollower(t, lead, local)
	d := cfg.Defaults
	if d.Model != "haiku" {
		t.Errorf("model = %q, want the local key", d.Model)
	}
	if d.MaxTurns != 9 {
		t.Errorf("max_turns = %d, want the lead's (an empty local value does not override)", d.MaxTurns)
	}
	if !reflect.DeepEqual(d.HarnessOptions, map[string]any{"permission_mode": "default"}) {
		t.Errorf("harness_options = %v, want the local map whole", d.HarnessOptions)
	}
	if n := d.Dispatch.MaxConcurrent; n == nil || *n != 2 {
		t.Errorf("dispatch.max_concurrent = %v, want local 2", n)
	}
	if d.Dispatch.ApprovalTimeout != "10m" {
		t.Errorf("dispatch.approval_timeout = %q, want the lead's sibling key", d.Dispatch.ApprovalTimeout)
	}
	if d.Push.Finished == nil || *d.Push.Finished {
		t.Errorf("push.finished = %v, want lead false", d.Push.Finished)
	}
}

func TestFollowLayerLocalActiveProfileUsesLeadProfiles(t *testing.T) {
	lead := "templates:\n  t1: {}\ndelegation:\n  active_profile: day\n  roles: {implement: {use_for: code}}\n  profiles:\n    day: {roles: {implement: t1}}\n    night: {roles: {implement: t1}}\n"
	cfg := loadFollower(t, lead, "delegation:\n  active_profile: night\n")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	res, err := cfg.ResolveRole("implement")
	if err != nil || res.Profile != "night" || res.Template != "t1" {
		t.Fatalf("ResolveRole = %+v, %v", res, err)
	}
	if cfg.Delegation.Roles["implement"].UseFor != "code" {
		t.Errorf("roles = %v, want the lead's", cfg.Delegation.Roles)
	}
}

func TestFollowLayerNoCacheIsLocalOnly(t *testing.T) {
	allowProbes(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "leo.yaml", followerHeader+"templates:\n  mine: {model: haiku}\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Templates) != 1 || cfg.TemplateSource("mine") != SourceLocal {
		t.Fatalf("templates = %v", cfg.Templates)
	}
	if w := strings.Join(cfg.FollowWarnings(), "\n"); !strings.Contains(w, "no lead config cached yet") {
		t.Fatalf("warnings = %q", w)
	}
}

func TestFollowLayerIgnoresCacheForOtherHost(t *testing.T) {
	allowProbes(t)
	dir := t.TempDir()
	writeLeadCache(t, dir, "templates:\n  x: {}\n")
	header := strings.Replace(followerHeader, "lead: {ssh", "other: {ssh: evan@10.0.2.11}\n    lead: {ssh", 1)
	header = strings.Replace(header, "host: lead", "host: other", 1)
	path := writeFile(t, dir, "leo.yaml", header)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Templates["x"]; ok {
		t.Fatal("a cache pulled from another host was layered")
	}
	if w := strings.Join(cfg.FollowWarnings(), "\n"); !strings.Contains(w, `cached from "lead"`) {
		t.Fatalf("warnings = %q", w)
	}
}

func TestNoFollowLoadIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	data := "defaults: {model: opus}\ntemplates:\n  a: {workspace: ~/a}\nenvironments:\n  e: {K: v}\n"
	path := writeFile(t, dir, "leo.yaml", data)
	// A stray cache must not matter without follow.
	writeLeadCache(t, dir, "templates:\n  x: {}\n")
	cfg, rev, err := LoadWithRevision(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := parse(path, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load without follow differs from a plain parse:\n got %#v\nwant %#v", cfg, want)
	}
	if rev != RevisionOf([]byte(data)) {
		t.Fatalf("revision changed without follow")
	}
	if cfg.IsFollower() || len(cfg.FollowWarnings()) != 0 {
		t.Fatal("non-follower reports follow state")
	}
}

// BenchmarkLoadFollower measures the lead layer's cost on Load, which runs
// at every config.Load call site.
func BenchmarkLoadFollower(b *testing.B) {
	allowProbes(b)
	lead := "templates:\n"
	for i := range 40 {
		lead += "  t" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ": {model: opus, workspace: ~/w, env: {A: b}}\n"
	}
	dir := b.TempDir()
	writeLeadCache(b, dir, lead+"environments:\n  e: {K: v}\ndefaults: {model: opus, max_turns: 3}\n")
	path := writeFile(b, dir, "leo.yaml", followerHeader+"templates:\n  mine: {model: haiku}\n")
	plain := writeFile(b, b.TempDir(), "leo.yaml", "templates:\n  mine: {model: haiku}\n")
	b.Run("follower", func(b *testing.B) {
		for b.Loop() {
			if _, err := Load(path); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("plain", func(b *testing.B) {
		for b.Loop() {
			if _, err := Load(plain); err != nil {
				b.Fatal(err)
			}
		}
	})
}
