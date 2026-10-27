package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// allowProbes makes every D4 probe pass: binaries on PATH, dirs present.
func allowProbes(t testing.TB) {
	t.Helper()
	t.Cleanup(SetFollowProbes(FollowProbes{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Stat:     func(string) (fs.FileInfo, error) { return nil, nil },
	}))
}

// writeRawLeadCache stores a hand-written export (sections only) as the
// cache, for lead content the lead's own Validate would refuse to export.
func writeRawLeadCache(t testing.TB, followerDir, sections string) {
	t.Helper()
	data := []byte(`{"schema":1,"kind":"leo.follow.export","leo_version":"v0.64.0","home_id":"ab","revision":"sha256:r",` + sections + `}`)
	exp, err := ParseFollowExport(data)
	if err != nil {
		t.Fatalf("raw export: %v", err)
	}
	err = WriteFollowCache(filepath.Join(followerDir, "state"), FollowCache{
		Host: "lead", PulledAt: time.Now(), LeadRevision: exp.Revision, Digest: exp.Digest(), Export: json.RawMessage(data),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func loadRawFollower(t *testing.T, sections, local string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	writeRawLeadCache(t, dir, sections)
	return Load(writeFile(t, dir, "leo.yaml", followerHeader+local))
}

func invalidReason(t *testing.T, err error) *InvalidEntryError {
	t.Helper()
	var inv *InvalidEntryError
	if !errors.As(err, &inv) {
		t.Fatalf("err = %v, want *InvalidEntryError", err)
	}
	return inv
}

func TestLeadTemplateWithUnknownHarnessIsIsolated(t *testing.T) {
	allowProbes(t)
	cfg, err := loadRawFollower(t, `"templates":{"bad":{"harness":"nope"},"good":{}}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed on an isolated lead entry: %v", err)
	}
	if _, ok := cfg.Templates["bad"]; ok {
		t.Fatal("invalid lead template is still usable")
	}
	_, err = cfg.LookupTemplate("bad")
	inv := invalidReason(t, err)
	if inv.Kind != KindTemplate || inv.Lead != "lead" || !strings.Contains(inv.Reason, `"nope" is not a registered harness`) {
		t.Fatalf("invalid = %+v", inv)
	}
	if !strings.Contains(err.Error(), `template "bad"`) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error text = %q", err)
	}
	if _, err := cfg.LookupTemplate("good"); err != nil {
		t.Fatalf("good: %v", err)
	}
	var unknown *UnknownEntryError
	if _, err := cfg.LookupTemplate("missing"); !errors.As(err, &unknown) {
		t.Fatalf("missing: %v, want *UnknownEntryError", err)
	}
	if got := cfg.InvalidEntries(KindTemplate); len(got) != 1 || got["bad"] == "" {
		t.Fatalf("InvalidEntries = %v", got)
	}
}

func TestLocalTemplateWithUnknownHarnessStillFails(t *testing.T) {
	allowProbes(t)
	cfg, err := loadRawFollower(t, `"templates":{"bad":{}}`, "templates:\n  bad: {harness: nope}\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `templates.bad.harness "nope"`) {
		t.Fatalf("Validate = %v, want the local entry to fail loudly", err)
	}
}

func TestD4ProbesMarkLeadEntriesInvalid(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	t.Cleanup(SetFollowProbes(FollowProbes{
		LookPath: func(name string) (string, error) {
			if name == "codex" {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		Stat: os.Stat,
	}))
	sections := `"templates":{"cx":{"harness":"codex","model":"gpt-5"},"cl":{}},` +
		`"environments":{"acct":{"CLAUDE_CONFIG_DIR":"` + missing + `"},"rel":{"CLAUDE_CONFIG_DIR":"$NOPE_UNSET/x"},"plain":{"A":"b"}}`
	cfg, err := loadRawFollower(t, sections, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	_, err = cfg.LookupTemplate("cx")
	if inv := invalidReason(t, err); !strings.Contains(inv.Reason, "codex") || !strings.Contains(inv.Reason, "PATH") {
		t.Fatalf("cx reason = %q", inv.Reason)
	}
	if _, err := cfg.LookupTemplate("cl"); err != nil {
		t.Fatalf("cl: %v", err)
	}
	_, err = cfg.MergeEnvironments([]string{"plain", "acct"})
	if inv := invalidReason(t, err); inv.Kind != KindEnvironment || !strings.Contains(inv.Reason, missing) {
		t.Fatalf("acct = %+v", inv)
	}
	if _, err := cfg.MergeEnvironments([]string{"rel", "plain"}); err != nil {
		t.Fatalf("a non-absolute config dir is not probed: %v", err)
	}
}

func TestAdvisoryBinaryProbeWarnsInsteadOfInvalidating(t *testing.T) {
	t.Cleanup(SetFollowProbes(FollowProbes{
		LookPath: func(name string) (string, error) { return "", errors.New("not found") },
		Stat:     os.Stat,
		// A process other than the daemon: its PATH says nothing about the
		// daemon's.
		BinaryAdvisory: true,
	}))
	cfg, err := loadRawFollower(t, `"templates":{"cx":{"harness":"codex","model":"gpt-5"}}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.LookupTemplate("cx"); err != nil {
		t.Fatalf("cx: %v, want usable (the daemon decides)", err)
	}
	warned := false
	for _, w := range cfg.FollowWarnings() {
		warned = warned || strings.Contains(w, "cx") && strings.Contains(w, "codex") && strings.Contains(w, "PATH")
	}
	if !warned {
		t.Fatalf("warnings = %q, want one naming cx, codex and PATH", cfg.FollowWarnings())
	}
}

func TestReferencesToInvalidLeadEntriesFailWithReason(t *testing.T) {
	allowProbes(t)
	sections := `"templates":{"bad":{"harness":"nope"}},"environments":{"badenv":{"1X":"y"}}`
	local := "tasks:\n  ping:\n    schedule: \"* * * * *\"\n    prompt_file: p.md\n    runtime: persistent\n    template: bad\n" +
		"templates:\n  mine:\n    environments: [badenv]\n"
	cfg, err := loadRawFollower(t, sections, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate must not fail on references to invalid lead entries: %v", err)
	}
	_, _, _, err = cfg.ResolveTaskTarget("ping")
	if inv := invalidReason(t, err); inv.Name != "bad" {
		t.Fatalf("task target = %+v", inv)
	}
	_, err = cfg.TemplateEnv(cfg.Templates["mine"], Expander{})
	if inv := invalidReason(t, err); inv.Name != "badenv" || !strings.Contains(inv.Reason, `"1X"`) {
		t.Fatalf("env = %+v", inv)
	}
}

func TestLeadProfileOverInvalidTemplateDoesNotCascade(t *testing.T) {
	allowProbes(t)
	sections := `"templates":{"bad":{"harness":"nope"},"good":{}},` +
		`"delegation":{"active_profile":"day","profiles":{"day":{"roles":{"implement":"bad","review":"good"}},"night":{"roles":{"implement":"good"}}}}`
	cfg, err := loadRawFollower(t, sections, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := cfg.LookupProfile("day"); err != nil {
		t.Fatalf("profile day cascaded into invalid: %v", err)
	}
	res, err := cfg.ResolveRole("review")
	if err != nil || res.Template != "good" {
		t.Fatalf("review = %+v, %v", res, err)
	}
	res, err = cfg.ResolveRole("implement")
	if err != nil {
		t.Fatalf("implement resolves to the template name; spawning it reports the reason: %v", err)
	}
	if _, err := cfg.LookupTemplate(res.Template); err == nil {
		t.Fatal("spawn target should be invalid")
	}
}

func TestInvalidLeadActiveProfile(t *testing.T) {
	allowProbes(t)
	sections := `"templates":{"good":{}},"delegation":{"active_profile":"day","profiles":{"day":{"roles":{"bad role":"good"}},"night":{"roles":{"implement":"good"}}}}`
	cfg, err := loadRawFollower(t, sections, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	_, err = cfg.ResolveRole("implement")
	if inv := invalidReason(t, err); inv.Kind != KindProfile || inv.Name != "day" {
		t.Fatalf("ResolveRole = %+v", inv)
	}
}

func TestBadLeadDefaultsKeyIsDropped(t *testing.T) {
	allowProbes(t)
	cfg, err := loadRawFollower(t, `"defaults":{"harness":"nope","max_turns":4,"dispatch":{"max_concurrent":-2,"approval_timeout":"5m"}}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Defaults.Harness != "" || cfg.DefaultsHarness() != "claude" {
		t.Fatalf("harness = %q, want the built-in default", cfg.Defaults.Harness)
	}
	if cfg.Defaults.MaxTurns != 4 || cfg.Defaults.Dispatch.ApprovalTimeout != "5m" {
		t.Fatalf("good lead keys lost: %+v", cfg.Defaults)
	}
	if cfg.Defaults.Dispatch.MaxConcurrent != nil {
		t.Fatalf("dispatch.max_concurrent = %v, want dropped", *cfg.Defaults.Dispatch.MaxConcurrent)
	}
	keys := cfg.InvalidLeadDefaults()
	if !strings.Contains(keys["harness"], "nope") || keys["dispatch.max_concurrent"] == "" || len(keys) != 2 {
		t.Fatalf("InvalidLeadDefaults = %v", keys)
	}
}

func TestBadLocalDefaultsKeyStillFails(t *testing.T) {
	allowProbes(t)
	cfg, err := loadRawFollower(t, `"defaults":{"max_turns":4}`, "defaults:\n  harness: nope\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `defaults.harness "nope"`) {
		t.Fatalf("Validate = %v", err)
	}
}
