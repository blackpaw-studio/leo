package web

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func mustConfigError(t *testing.T, err error) *ConfigError {
	t.Helper()
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %#v, want *ConfigError", err)
	}
	return ce
}

func TestMutateStaleRevisionWritesNothing(t *testing.T) {
	svc, path := newConfigService(t, nil)
	before, _ := os.ReadFile(path)

	_, err := svc.Mutate(mutateOpts{IfMatch: "sha256:stale"}, func(cfg *config.Config) error {
		cfg.Defaults.Model = "opus"
		return nil
	})
	ce := mustConfigError(t, err)
	if ce.Status != http.StatusConflict || ce.Code != CodeRevisionMismatch {
		t.Fatalf("got %d %q, want 409 revision_mismatch", ce.Status, ce.Code)
	}
	if data, ok := ce.Data.(map[string]string); !ok || data["revision"] != revisionOnDisk(t, path) {
		t.Errorf("409 data = %#v, want the current revision", ce.Data)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a stale write changed the file")
	}
}

func TestMutateMatchingRevisionWritesAndReturnsNewRevision(t *testing.T) {
	svc, path := newConfigService(t, nil)
	rev := revisionOnDisk(t, path)

	res, err := svc.Mutate(mutateOpts{IfMatch: rev}, func(cfg *config.Config) error {
		cfg.Defaults.Model = "opus"
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if res.Revision == rev || res.Revision != revisionOnDisk(t, path) {
		t.Errorf("revision = %q, want the new on-disk revision (old %q)", res.Revision, rev)
	}
	cfg, _ := config.Load(path)
	if cfg.Defaults.Model != "opus" {
		t.Errorf("model = %q, want opus", cfg.Defaults.Model)
	}
}

func TestMutateEmptyIfMatchIsUnchecked(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	if _, err := svc.Mutate(mutateOpts{}, func(cfg *config.Config) error { cfg.Defaults.MaxTurns = 3; return nil }); err != nil {
		t.Fatalf("an unchecked write failed: %v", err)
	}
}

func TestMutateInvalidConfigMapsFieldsAndWritesNothing(t *testing.T) {
	svc, path := newConfigService(t, nil)
	before, _ := os.ReadFile(path)

	_, err := svc.Mutate(mutateOpts{Scope: validationScope{Resource: "templates", Name: "coder"}}, func(cfg *config.Config) error {
		t := cfg.Templates["coder"]
		t.MaxTurns = -1
		cfg.Templates["coder"] = t
		return nil
	})
	ce := mustConfigError(t, err)
	if ce.Status != http.StatusUnprocessableEntity || ce.Code != CodeInvalid {
		t.Fatalf("got %d %q, want 422 invalid", ce.Status, ce.Code)
	}
	if len(ce.Fields) != 1 || ce.Fields[0].Field != "max_turns" {
		t.Errorf("fields = %+v, want one on max_turns", ce.Fields)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("an invalid write changed the file")
	}
}

func TestMutateErrorsOutsideTheEditedResourceAreFormLevel(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	_, err := svc.Mutate(mutateOpts{Scope: validationScope{Resource: "defaults"}}, func(cfg *config.Config) error {
		t := cfg.Templates["coder"]
		t.MaxTurns = -1
		cfg.Templates["coder"] = t
		return nil
	})
	ce := mustConfigError(t, err)
	if len(ce.Fields) != 1 || ce.Fields[0].Field != "" {
		t.Errorf("fields = %+v, want one form-level error", ce.Fields)
	}
}

func TestMutateReportsReloadFailureAsWarning(t *testing.T) {
	svc, _ := newConfigService(t, &mockReloader{err: errors.New("boom")})
	res, err := svc.Mutate(mutateOpts{}, func(cfg *config.Config) error { cfg.Defaults.MaxTurns = 4; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Warning == "" {
		t.Error("a failed reload must surface as a warning, the write still stands")
	}
}

func TestMutateRaisesTheRequestedFlag(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	res, err := svc.Mutate(mutateOpts{Flag: &svc.agentsRestartNeeded}, func(cfg *config.Config) error { cfg.Defaults.MaxTurns = 4; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !res.Restart.Agents || res.Restart.Service {
		t.Errorf("restart = %+v, want agents only", res.Restart)
	}
}

func TestMutateUnloadableConfigIs503(t *testing.T) {
	svc, path := newConfigService(t, nil)
	if err := os.WriteFile(path, []byte("defaults: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Mutate(mutateOpts{}, func(*config.Config) error { return nil })
	if ce := mustConfigError(t, err); ce.Status != http.StatusServiceUnavailable || ce.Code != CodeConfigUnavailable {
		t.Errorf("got %d %q, want 503 config_unavailable", ce.Status, ce.Code)
	}
}

func TestApplyValuesUpdatesTemplateAndFlagsAgents(t *testing.T) {
	svc, path := newConfigService(t, nil)
	form := url.Values{"model": {"sonnet"}, "workspace": {"/tmp/coder"}}
	res, err := svc.ApplyValues("", resourceByKey("templates"), "coder", form, nil)
	if err != nil {
		t.Fatalf("ApplyValues: %v", err)
	}
	if !res.Restart.Agents {
		t.Error("a template save must flag the agents restart")
	}
	cfg, _ := config.Load(path)
	if cfg.Templates["coder"].Model != "sonnet" {
		t.Errorf("model = %q, want sonnet", cfg.Templates["coder"].Model)
	}
}

func TestApplyValuesPinsParseErrorsToTheirField(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	_, err := svc.ApplyValues("", resourceByKey("tasks"), "heartbeat", url.Values{"schedule": {"0 * * * *"}, "retries": {"x"}}, nil)
	ce := mustConfigError(t, err)
	if ce.Status != http.StatusUnprocessableEntity || len(ce.Fields) != 1 || ce.Fields[0].Field != "retries" {
		t.Errorf("got %d %+v, want 422 on retries", ce.Status, ce.Fields)
	}
}

func TestApplyValuesUnknownItemIs404(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	_, err := svc.ApplyValues("", resourceByKey("templates"), "nope", url.Values{}, nil)
	if ce := mustConfigError(t, err); ce.Status != http.StatusNotFound || ce.Code != CodeNotFound {
		t.Errorf("got %d %q, want 404 not_found", ce.Status, ce.Code)
	}
}

func TestApplyValuesPrefixesSubsectionAndHarnessErrors(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	form := url.Values{"model": {"opus"}, "harness_options.bypass_permissions": {"maybe"}}
	_, err := svc.ApplyValues("", resourceByKey("templates"), "coder", form, nil)
	ce := mustConfigError(t, err)
	if len(ce.Fields) != 1 || ce.Fields[0].Field != "harness_options.bypass_permissions" {
		t.Errorf("fields = %+v, want harness_options.bypass_permissions", ce.Fields)
	}
}

func TestAddItemRejectsBadAndTakenNames(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	_, err := svc.AddItem("", resourceByKey("templates"), "bad name", nil)
	if ce := mustConfigError(t, err); ce.Status != http.StatusUnprocessableEntity || ce.Fields[0].Field != "name" {
		t.Errorf("bad name: got %d %+v", ce.Status, ce.Fields)
	}
	_, err = svc.AddItem("", resourceByKey("templates"), "coder", nil)
	if ce := mustConfigError(t, err); ce.Status != http.StatusUnprocessableEntity || ce.Fields[0].Field != "name" {
		t.Errorf("taken name must be 422 on name, got %d %+v", ce.Status, ce.Fields)
	}
}

func TestAddTaskRequiresScheduleAndDefaultsPromptFile(t *testing.T) {
	svc, path := newConfigService(t, nil)
	_, err := svc.AddItem("", resourceByKey("tasks"), "fresh", map[string]string{})
	if ce := mustConfigError(t, err); len(ce.Fields) != 1 || ce.Fields[0].Field != "schedule" {
		t.Errorf("missing schedule: %+v", ce.Fields)
	}
	if _, err := svc.AddItem("", resourceByKey("tasks"), "fresh", map[string]string{"schedule": "5 * * * *"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	task := cfg.Tasks["fresh"]
	if task.PromptFile != "prompts/fresh.md" || task.Enabled {
		t.Errorf("new task = %+v, want prompts/fresh.md and disabled", task)
	}
}

func TestRenameTemplateRewritesReferencesAndNoopSkipsWrite(t *testing.T) {
	svc, path := newConfigService(t, nil)
	if _, err := svc.RenameItem("", resourceByKey("templates"), "reviewer", "checker"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	cfg, _ := config.Load(path)
	if _, ok := cfg.Templates["checker"]; !ok {
		t.Fatal("template not re-keyed")
	}
	if cfg.Templates["coder"].Permissions.CanSpawn[0] != "checker" || cfg.Delegation.Profiles["fast"].Roles["coder"].Template != "checker" {
		t.Error("references were not rewritten")
	}
	rev := revisionOnDisk(t, path)
	res, err := svc.RenameItem("", resourceByKey("templates"), "checker", "checker")
	if err != nil || res.Revision != rev {
		t.Errorf("no-op rename: res=%+v err=%v, want unchanged revision", res, err)
	}
}

func TestDuplicateTemplateDeepCopiesEverything(t *testing.T) {
	svc, path := newConfigService(t, nil)
	if _, err := svc.DuplicateItem("", resourceByKey("templates"), "coder", "coder2"); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	cfg, _ := config.Load(path)
	a, b := cfg.Templates["coder"], cfg.Templates["coder2"]
	if b.Model != a.Model || b.HarnessOptions["permission_mode"] != "plan" || len(b.Permissions.CanSpawn) != 1 {
		t.Errorf("copy lost data: %+v", b)
	}
	b.HarnessOptions["permission_mode"] = "auto"
	if a.HarnessOptions["permission_mode"] != "plan" {
		t.Error("the copy shares its harness_options map with the original")
	}
	_, err := svc.DuplicateItem("", resourceByKey("templates"), "coder", "reviewer")
	if ce := mustConfigError(t, err); ce.Fields[0].Field != "new_name" {
		t.Errorf("taken duplicate name: %+v", ce.Fields)
	}
}

func TestDeleteItemUnknownIs404AndReferencedEnvironmentIs422(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	_, err := svc.DeleteItem("", resourceByKey("tasks"), "nope")
	if ce := mustConfigError(t, err); ce.Status != http.StatusNotFound {
		t.Errorf("unknown delete: %d", ce.Status)
	}
	_, err = svc.DeleteItem("", resourceByKey("environments"), "base")
	if ce := mustConfigError(t, err); ce.Status != http.StatusUnprocessableEntity {
		t.Errorf("deleting a referenced environment: %d, want 422", ce.Status)
	}
}
