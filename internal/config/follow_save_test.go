package config

import (
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
)

const saveLeadYAML = `defaults:
  model: opus
  max_turns: 9
  dispatch: {max_concurrent: 4}
templates:
  lt: {model: opus}
  shared-name: {model: opus}
environments:
  le: {A: lead}
delegation:
  active_profile: day
  profiles:
    day: {roles: {implement: shared-name, review: lt}}
    night: {roles: {implement: lt}}
`

const saveLocalYAML = `defaults:
  model: haiku
templates:
  mine: {model: haiku}
  shared-name: {model: haiku}
environments:
  me: {B: local}
delegation:
  active_profile: night
  profiles:
    solo: {roles: {implement: mine}}
`

// followerOnDisk writes a follower (followerHeader + local) over a cached
// lead and returns its path.
func followerOnDisk(t *testing.T, lead, local string) string {
	t.Helper()
	allowProbes(t)
	dir := t.TempDir()
	writeLeadCache(t, dir, lead)
	return writeFile(t, dir, "leo.yaml", followerHeader+local)
}

// hasKeyLine reports whether some line of a YAML document is "<key>:...".
func hasKeyLine(text, key string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key+":") {
			return true
		}
	}
	return false
}

func mustLoad(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestFollowerSaveWithoutEditsIsByteIdentical(t *testing.T) {
	path := followerOnDisk(t, saveLeadYAML, saveLocalYAML)
	before := readFile(t, path)
	cfg := mustLoad(t, path)
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if after := readFile(t, path); after != before {
		t.Fatalf("no-op save rewrote leo.yaml:\n%s", after)
	}
	// Even a full rewrite (a local edit) must not leak lead entries.
	cfg.Templates["mine"] = TemplateConfig{Model: "sonnet"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, path)
	for _, key := range []string{"lt", "le", "day"} {
		if hasKeyLine(after, key) {
			t.Errorf("leo.yaml gained lead entry %q:\n%s", key, after)
		}
	}
	for _, lead := range []string{"max_turns: 9", "opus", "max_concurrent", "implement: lt"} {
		if strings.Contains(after, lead) {
			t.Errorf("leo.yaml gained lead content %q:\n%s", lead, after)
		}
	}
	re := mustLoad(t, path)
	if re.TemplateSource("lt") != SourceLead || re.EntrySource(KindEnvironment, "le") != SourceLead || re.EntrySource(KindProfile, "day") != SourceLead {
		t.Fatal("lead entries lost their source after a save")
	}
	if re.Defaults.MaxTurns != 9 || re.Delegation.ActiveProfile != "night" || re.Defaults.Model != "haiku" {
		t.Fatalf("reload = %+v / %q", re.Defaults, re.Delegation.ActiveProfile)
	}
}

func TestLeadEntryWritesAreOwnedByLead(t *testing.T) {
	cases := []struct {
		name string
		kind EntryKind
		op   string
		edit func(*Config) error
	}{
		{"edit template", KindTemplate, "edit", func(c *Config) error { c.Templates["lt"] = TemplateConfig{Model: "haiku"}; return nil }},
		{"delete template", KindTemplate, "delete", func(c *Config) error { delete(c.Templates, "lt"); return nil }},
		{"rename template", KindTemplate, "delete", func(c *Config) error { return RenameTemplate(c, "lt", "lt2") }},
		{"edit environment", KindEnvironment, "edit", func(c *Config) error { c.Environments["le"]["A"] = "x"; return nil }},
		{"delete environment", KindEnvironment, "delete", func(c *Config) error { delete(c.Environments, "le"); return nil }},
		{"edit profile", KindProfile, "edit", func(c *Config) error {
			c.Delegation.Profiles["night"].Roles["implement"] = RoleTarget{Template: "mine"}
			return nil
		}},
		{"delete profile", KindProfile, "delete", func(c *Config) error { delete(c.Delegation.Profiles, "night"); return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := followerOnDisk(t, saveLeadYAML, saveLocalYAML)
			before := readFile(t, path)
			cfg := mustLoad(t, path)
			if err := tc.edit(cfg); err != nil {
				t.Fatal(err)
			}
			err := Save(path, cfg)
			var owned *OwnedByLeadError
			if !errors.As(err, &owned) || owned.Kind != tc.kind || owned.Op != tc.op || owned.Lead != "lead" {
				t.Fatalf("Save = %v, want OwnedByLeadError{%s %s}", err, tc.op, tc.kind)
			}
			if readFile(t, path) != before {
				t.Fatal("refused save changed leo.yaml")
			}
		})
	}
}

func TestRenameLocalTemplateNamedByLeadProfileNamesTheProfile(t *testing.T) {
	path := followerOnDisk(t, saveLeadYAML, saveLocalYAML)
	cfg := mustLoad(t, path)
	if err := RenameTemplate(cfg, "shared-name", "renamed"); err != nil {
		t.Fatal(err)
	}
	err := Save(path, cfg)
	var owned *OwnedByLeadError
	if !errors.As(err, &owned) || owned.Kind != KindProfile || owned.Name != "day" {
		t.Fatalf("Save = %v, want the lead profile day named", err)
	}
	if !strings.Contains(err.Error(), `profile "day"`) {
		t.Fatalf("message = %q", err)
	}
}

func TestOverrideLocallyThenEditAndRevert(t *testing.T) {
	path := followerOnDisk(t, saveLeadYAML, saveLocalYAML)
	cfg := mustLoad(t, path)
	for _, k := range []struct {
		kind EntryKind
		name string
	}{{KindTemplate, "lt"}, {KindEnvironment, "le"}, {KindProfile, "night"}} {
		if err := cfg.OverrideLocally(k.kind, k.name); err != nil {
			t.Fatalf("override %s %s: %v", k.kind, k.name, err)
		}
	}
	cfg.Templates["lt"] = TemplateConfig{Model: "sonnet"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	re := mustLoad(t, path)
	if re.TemplateSource("lt") != SourceLocalOverrideLead || re.Templates["lt"].Model != "sonnet" {
		t.Fatalf("lt = %q %+v", re.TemplateSource("lt"), re.Templates["lt"])
	}
	if re.EntrySource(KindEnvironment, "le") != SourceLocalOverrideLead || re.EntrySource(KindProfile, "night") != SourceLocalOverrideLead {
		t.Fatal("unedited overrides were not written locally")
	}
	if !hasKeyLine(readFile(t, path), "lt") {
		t.Fatal("override not in leo.yaml")
	}
	// Deleting the override shows the lead's entry again.
	delete(re.Templates, "lt")
	if err := Save(path, re); err != nil {
		t.Fatal(err)
	}
	back := mustLoad(t, path)
	if back.TemplateSource("lt") != SourceLead || back.Templates["lt"].Model != "opus" {
		t.Fatalf("lt after delete = %q %+v", back.TemplateSource("lt"), back.Templates["lt"])
	}
}

func TestOverrideLocallyRefusals(t *testing.T) {
	allowProbes(t)
	cfg, err := loadRawFollower(t, `"templates":{"bad":{"harness":"nope"},"ok":{}}`, "templates:\n  mine: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.OverrideLocally(KindTemplate, "bad"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("override of an invalid lead entry = %v (D6 refuses it)", err)
	}
	if err := cfg.OverrideLocally(KindTemplate, "mine"); err == nil {
		t.Fatal("override of a local entry succeeded")
	}
	if err := cfg.OverrideLocally(KindTemplate, "ghost"); err == nil {
		t.Fatal("override of a missing entry succeeded")
	}
}

func TestEditingLeadDefaultsKeyWritesLocalKey(t *testing.T) {
	path := followerOnDisk(t, saveLeadYAML, saveLocalYAML)
	cfg := mustLoad(t, path)
	cfg.Defaults.IdleSuspendAfter = "2h"
	n := 1
	cfg.Defaults.Dispatch.MaxConcurrent = &n
	cfg.Delegation.ActiveProfile = "day"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	for _, want := range []string{"idle_suspend_after: 2h", "max_concurrent: 1", "active_profile: day"} {
		if !strings.Contains(text, want) {
			t.Errorf("leo.yaml lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "max_turns: 9") {
		t.Errorf("an untouched lead key was written:\n%s", text)
	}
	re := mustLoad(t, path)
	if re.Defaults.IdleSuspendAfter != "2h" || re.DispatchMaxConcurrent() != 1 || re.Defaults.MaxTurns != 9 {
		t.Fatalf("reload defaults = %+v", re.Defaults)
	}
}

func TestInvalidLeadEntriesAreNeverWritten(t *testing.T) {
	allowProbes(t)
	dir := t.TempDir()
	writeRawLeadCache(t, dir, `"templates":{"bad":{"harness":"nope"}},"environments":{"badenv":{"1X":"y"}}`)
	path := writeFile(t, dir, "leo.yaml", followerHeader+"templates:\n  mine: {}\n")
	cfg := mustLoad(t, path)
	cfg.Templates["mine"] = TemplateConfig{Model: "haiku"}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if text := readFile(t, path); strings.Contains(text, "bad") {
		t.Fatalf("invalid lead entry written:\n%s", text)
	}
}

func TestInheritFollowKeepsTheLayer(t *testing.T) {
	path := followerOnDisk(t, saveLeadYAML, saveLocalYAML)
	existing := mustLoad(t, path)
	// The setup wizard rebuilds a config from scratch, keeping templates.
	rebuilt := &Config{Defaults: existing.Defaults, Client: existing.Client, Templates: maps.Clone(existing.Templates), Tasks: map[string]TaskConfig{}}
	rebuilt.InheritFollow(existing)
	if err := Save(path, rebuilt); err != nil {
		t.Fatalf("Save: %v", err)
	}
	re := mustLoad(t, path)
	if !re.IsFollower() || re.TemplateSource("lt") != SourceLead || re.EntrySource(KindEnvironment, "le") != SourceLead {
		t.Fatalf("layer lost on a setup re-run: follow=%v lt=%q", re.Follow, re.TemplateSource("lt"))
	}
	if text := readFile(t, path); hasKeyLine(text, "lt") {
		t.Fatalf("lead template flattened into leo.yaml:\n%s", text)
	}
}

func TestSaveWithoutFollowIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "leo.yaml", "templates:\n  a: {model: opus}\n")
	cfg := mustLoad(t, path)
	if err := cfg.CheckLeadOwnership(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.OverrideLocally(KindTemplate, "a"); err == nil {
		t.Fatal("override without follow succeeded")
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
