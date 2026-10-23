package web

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/history"
)

var updateGolden = flag.Bool("update", false, "rewrite internal/web/testdata/configapi golden files")

var runAtPattern = regexp.MustCompile(`"run_at":"[^"]*"`)

const goldenDir = "testdata/configapi"

// checkGolden compares body against testdata/configapi/<name>.json, written by
// `go test ./internal/web -run TestConfigAPIGolden -update`. The files are the
// wire contract the app's decoder tests are built from (leoterm copies them
// as its fixtures), so any diff is an API change a reviewer must see.
func checkGolden(t *testing.T, name string, res apiResult, home string) {
	t.Helper()
	raw := bytes.ReplaceAll(res.Raw, []byte(home), []byte("<home>"))
	// history stamps run_at from the wall clock.
	raw = runAtPattern.ReplaceAll(raw, []byte(`"run_at":"2026-10-23T08:00:01Z"`))
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s: body is not JSON: %v\n%s", name, err, raw)
	}
	pretty, err := json.MarshalIndent(map[string]any{"status": res.Status, "body": decoded}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pretty = append(pretty, '\n')
	path := filepath.Join(goldenDir, name+".json")
	if *updateGolden {
		if err := os.MkdirAll(goldenDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update to create it)", path, err)
	}
	if string(want) != string(pretty) {
		t.Errorf("%s changed; run with -update and review the diff\n--- want\n%s\n--- got\n%s", path, want, pretty)
	}
}

func TestConfigAPIGolden(t *testing.T) {
	h := newAPIHarness(t)
	home := filepath.Dir(h.path)
	cfg := h.loadConfig()

	// Deterministic task state: one history entry, one prompt file.
	store := history.NewStore(cfg.HomePath)
	if err := store.RecordTimed("heartbeat", 0, "", "heartbeat-1.jsonl", time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC), 1500); err != nil {
		t.Fatal(err)
	}
	ws := cfg.TaskWorkspace(cfg.Tasks["heartbeat"])
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "heartbeat.md"), []byte("Check the thing.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.svc.version = func() string { return "v0.0.0-golden" }
	h.svc.pid, h.svc.startedAt = 1234, time.Date(2026, 10, 23, 7, 0, 0, 0, time.UTC)

	get := func(name, path string) {
		t.Run(name, func(t *testing.T) { checkGolden(t, name, h.do(http.MethodGet, path, "", nil), home) })
	}
	get("schema", "/config/schema")
	get("defaults", "/config/defaults")
	get("templates", "/config/templates")
	get("template_coder", "/config/templates/coder")
	get("tasks", "/config/tasks")
	get("environments", "/config/environments")
	get("web", "/config/web")
	get("client", "/config/client")
	get("hosts", "/config/hosts")
	get("delegation", "/config/delegation")
	get("delegation_preview", "/config/delegation/preview?profile=deep")
	get("task_history", "/tasks/heartbeat/history")
	get("prompt", "/config/tasks/heartbeat/prompt")
	get("cron_preview", "/cron/preview?expr=0+3+*+*+*")
	get("service_status", "/service/status")

	// Errors a client must render: stale revision, no revision, field errors.
	t.Run("error_409", func(t *testing.T) {
		checkGolden(t, "error_409", h.do(http.MethodPut, "/config/defaults", "sha256:stale", map[string]any{"values": map[string]any{"model": "opus"}}), home)
	})
	t.Run("error_428", func(t *testing.T) {
		checkGolden(t, "error_428", h.do(http.MethodPut, "/config/defaults", "", map[string]any{"values": map[string]any{"model": "opus"}}), home)
	})
	t.Run("error_422", func(t *testing.T) {
		checkGolden(t, "error_422", h.put("/config/tasks/heartbeat", map[string]any{"schedule": "not a cron", "retries": -1}), home)
	})
	t.Run("error_400", func(t *testing.T) {
		checkGolden(t, "error_400", h.put("/config/defaults", map[string]any{"no_such_field": true}), home)
	})
	t.Run("error_404", func(t *testing.T) {
		checkGolden(t, "error_404", h.do(http.MethodGet, "/config/templates/ghost", "", nil), home)
	})

	// Writes: the response carries the new revision, restart class and item.
	t.Run("put_defaults", func(t *testing.T) {
		checkGolden(t, "put_defaults", h.put("/config/defaults", map[string]any{"model": "opus"}), home)
	})
	t.Run("put_web", func(t *testing.T) {
		checkGolden(t, "put_web", h.put("/config/web", map[string]any{"port": 9000}), home)
	})
	t.Run("post_task", func(t *testing.T) {
		checkGolden(t, "post_task", h.do(http.MethodPost, "/config/tasks", h.revision(), map[string]any{"name": "weekly", "schedule": "0 9 * * 1"}), home)
	})
	t.Run("post_template_duplicate", func(t *testing.T) {
		checkGolden(t, "post_template_duplicate", h.do(http.MethodPost, "/config/templates/coder/duplicate", h.revision(), map[string]any{"new_name": "coder2"}), home)
	})
	t.Run("post_template_rename", func(t *testing.T) {
		checkGolden(t, "post_template_rename", h.do(http.MethodPost, "/config/templates/coder2/rename", h.revision(), map[string]any{"new_name": "coder3"}), home)
	})
	t.Run("delegation_cell", func(t *testing.T) {
		checkGolden(t, "delegation_cell", h.do(http.MethodPut, "/config/delegation/profiles/fast/roles/coder", h.revision(),
			map[string]any{"template": "coder", "effort": "high"}), home)
	})
	t.Run("delegation_cell_422", func(t *testing.T) {
		checkGolden(t, "delegation_cell_422", h.do(http.MethodPut, "/config/delegation/profiles/fast/roles/coder", h.revision(),
			map[string]any{"template": "coder", "effort": "ludicrous"}), home)
	})
	t.Run("put_template_env_secret", func(t *testing.T) {
		checkGolden(t, "put_template_env_secret", h.put("/config/templates/coder", map[string]any{"env": map[string]any{
			"ANTHROPIC_AUTH_TOKEN": "sk-golden-never-returned", "ANTHROPIC_BASE_URL": "http://localhost:3325",
		}}), home)
	})
	t.Run("prompt_put", func(t *testing.T) {
		rev := h.do(http.MethodGet, "/config/tasks/heartbeat/prompt", "", nil).data()["revision"].(string)
		checkGolden(t, "prompt_put", h.do(http.MethodPut, "/config/tasks/heartbeat/prompt", rev, map[string]any{"content": "New body.\n"}), home)
	})
}
