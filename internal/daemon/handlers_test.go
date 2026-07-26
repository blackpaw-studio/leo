package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
)

const testConfigYAML = `
agent:
  name: test-agent
  workspace: /tmp/test-workspace
defaults:
  model: sonnet
  max_turns: 10
tasks:
  heartbeat:
    schedule: "0 * * * *"
    prompt_file: heartbeat.md
    enabled: true
    env:
      OP_SERVICE_ACCOUNT_TOKEN: ` + testSecretEnvValue + `
      ANTHROPIC_BASE_URL: http://localhost:3325
`

// testSecretEnvValue is an obviously-fake credential planted in the task
// fixture so the leak guard below can assert it never reaches a response.
const testSecretEnvValue = "ops_totally_fake_token_do_not_use"

func writeTestConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return cfgPath
}

func startTestServer(t *testing.T, cfgPath string) (*Server, *http.Client) {
	t.Helper()
	sockPath := tmpSockPath(t, "h.sock")
	s := New(sockPath, cfgPath, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck
	return s, socketHTTPClient(sockPath)
}

func TestHandleCronInstall(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Post("http://localhost/cron/install", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /cron/install error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}
}

func TestHandleCronRemove(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	// Install first
	resp, err := client.Post("http://localhost/cron/install", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /cron/install error: %v", err)
	}
	resp.Body.Close()

	// Remove
	resp, err = client.Post("http://localhost/cron/remove", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /cron/remove error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}
}

func TestHandleCronList(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	// Install first so there are entries to list
	resp, err := client.Post("http://localhost/cron/install", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /cron/install error: %v", err)
	}
	resp.Body.Close()

	// List
	resp, err = client.Get("http://localhost/cron/list")
	if err != nil {
		t.Fatalf("GET /cron/list error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}

	var entries []cron.EntryInfo
	if err := json.Unmarshal(body.Data, &entries); err != nil {
		t.Fatalf("unmarshaling entries: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "heartbeat" {
		t.Errorf("expected entry name 'heartbeat', got %q", entries[0].Name)
	}
	if entries[0].Schedule != "0 * * * *" {
		t.Errorf("expected schedule '0 * * * *', got %q", entries[0].Schedule)
	}
}

func TestHandleTaskAdd(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskAddRequest{
		Name:       "daily",
		Schedule:   "0 9 * * *",
		PromptFile: "daily.md",
		Enabled:    true,
	})

	resp, err := client.Post("http://localhost/task/add", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/add error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	task, ok := cfg.Tasks["daily"]
	if !ok {
		t.Fatal("expected task 'daily' to exist in config")
	}
	if task.Schedule != "0 9 * * *" {
		t.Errorf("expected schedule %q, got %q", "0 9 * * *", task.Schedule)
	}
	if task.PromptFile != "daily.md" {
		t.Errorf("expected prompt_file %q, got %q", "daily.md", task.PromptFile)
	}
	if !task.Enabled {
		t.Error("expected task to be enabled")
	}
}

func TestHandleTaskRemove(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})

	resp, err := client.Post("http://localhost/task/remove", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/remove error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if _, ok := cfg.Tasks["heartbeat"]; ok {
		t.Error("expected task 'heartbeat' to be removed from config")
	}
}

func TestHandleTaskRemoveNotFound(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "nonexistent"})

	resp, err := client.Post("http://localhost/task/remove", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/remove error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for not found task")
	}
}

func TestHandleTaskEnable(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Write config with heartbeat disabled
	cfgYAML := `
agent:
  name: test-agent
  workspace: /tmp/test-workspace
defaults:
  model: sonnet
  max_turns: 10
tasks:
  heartbeat:
    schedule: "0 * * * *"
    prompt_file: heartbeat.md
    enabled: false
`
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})

	resp, err := client.Post("http://localhost/task/enable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/enable error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if !cfg.Tasks["heartbeat"].Enabled {
		t.Error("expected task 'heartbeat' to be enabled after POST /task/enable")
	}
}

func TestHandleTaskDisable(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir) // heartbeat is enabled: true
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})

	resp, err := client.Post("http://localhost/task/disable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/disable error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if cfg.Tasks["heartbeat"].Enabled {
		t.Error("expected task 'heartbeat' to be disabled after POST /task/disable")
	}
}

func TestHandleTaskList(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Get("http://localhost/task/list")
	if err != nil {
		t.Fatalf("GET /task/list error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	var tasks []taskListInfo
	if err := json.Unmarshal(result.Data, &tasks); err != nil {
		t.Fatalf("unmarshaling tasks: %v", err)
	}

	if len(tasks) != 1 || tasks[0].Name != "heartbeat" {
		t.Errorf("expected 'heartbeat' task in list response, got %+v", tasks)
	}
}

func TestHandleTaskAddMalformedJSON(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Post("http://localhost/task/add", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("POST /task/add error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for malformed JSON")
	}
}

func TestHandleTaskAddMissingName(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskAddRequest{
		Schedule:   "0 9 * * *",
		PromptFile: "daily.md",
	})

	resp, err := client.Post("http://localhost/task/add", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/add error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for missing name")
	}
	if !strings.Contains(result.Error, "name") {
		t.Errorf("error = %q, want to contain 'name'", result.Error)
	}
}

func TestHandleTaskAddMissingSchedule(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskAddRequest{
		Name:       "test",
		PromptFile: "daily.md",
	})

	resp, err := client.Post("http://localhost/task/add", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/add error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for missing schedule")
	}
}

func TestHandleTaskAddMissingPromptFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskAddRequest{
		Name:     "test",
		Schedule: "* * * * *",
	})

	resp, err := client.Post("http://localhost/task/add", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/add error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for missing prompt_file")
	}
}

func TestHandleTaskRemoveMalformedJSON(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Post("http://localhost/task/remove", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("POST /task/remove error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for malformed JSON")
	}
}

func TestHandleTaskEnableNotFound(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "nonexistent"})

	resp, err := client.Post("http://localhost/task/enable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/enable error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for not found task")
	}
}

func TestHandleTaskDisableNotFound(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "nonexistent"})

	resp, err := client.Post("http://localhost/task/disable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/disable error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for not found task")
	}
}

func TestHandleTaskListEmpty(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Write config with no tasks
	cfgYAML := `
agent:
  name: test-agent
  workspace: /tmp/test-workspace
defaults:
  model: sonnet
  max_turns: 10
`
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	_, client := startTestServer(t, cfgPath)

	resp, err := client.Get("http://localhost/task/list")
	if err != nil {
		t.Fatalf("GET /task/list error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}
}

func TestHandleConfigLoadError(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Write a valid config so the server can start, then delete it
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	// Remove the config file so handlers that load config will fail
	if err := os.Remove(cfgPath); err != nil {
		t.Fatalf("removing config: %v", err)
	}

	// Test task/list
	resp, err := client.Get("http://localhost/task/list")
	if err != nil {
		t.Fatalf("GET /task/list error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for config load error")
	}

	// Test task/add config load error
	addBody, _ := json.Marshal(TaskAddRequest{
		Name:       "test",
		Schedule:   "* * * * *",
		PromptFile: "test.md",
	})
	resp2, err := client.Post("http://localhost/task/add", "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("POST /task/add error: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusInternalServerError {
		t.Errorf("task/add: expected status 500, got %d", resp2.StatusCode)
	}

	// Test task/remove config load error
	removeBody, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})
	resp3, err := client.Post("http://localhost/task/remove", "application/json", bytes.NewReader(removeBody))
	if err != nil {
		t.Fatalf("POST /task/remove error: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusInternalServerError {
		t.Errorf("task/remove: expected status 500, got %d", resp3.StatusCode)
	}

	// Test task/enable config load error
	enableBody, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})
	resp4, err := client.Post("http://localhost/task/enable", "application/json", bytes.NewReader(enableBody))
	if err != nil {
		t.Fatalf("POST /task/enable error: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusInternalServerError {
		t.Errorf("task/enable: expected status 500, got %d", resp4.StatusCode)
	}

	// Test cron/install config load error
	resp5, err := client.Post("http://localhost/cron/install", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /cron/install error: %v", err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusInternalServerError {
		t.Errorf("cron/install: expected status 500, got %d", resp5.StatusCode)
	}
}

func TestHandleTaskEnableMalformedJSON(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Post("http://localhost/task/enable", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("POST /task/enable error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for malformed JSON")
	}
}

// TestHandleTaskListOmitsEnvValues guards a credential leak: GET /task/list is
// served on the daemon's Unix socket, which every agent can reach with a
// one-line curl. Task env holds credentials, so the response carries a trimmed
// projection (env key names only), never the values.
func TestHandleTaskListOmitsEnvValues(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Get("http://localhost/task/list")
	if err != nil {
		t.Fatalf("GET /task/list: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if strings.Contains(body.String(), testSecretEnvValue) {
		t.Errorf("task list leaked a secret env value; body: %s", body.String())
	}

	var envelope Response
	if err := json.Unmarshal(body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding envelope: %v", err)
	}
	var tasks []taskListInfo
	if err := json.Unmarshal(envelope.Data, &tasks); err != nil {
		t.Fatalf("decoding tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d: %+v", len(tasks), tasks)
	}
	if tasks[0].Name != "heartbeat" || tasks[0].Schedule != "0 * * * *" || !tasks[0].Enabled {
		t.Errorf("task projection = %+v", tasks[0])
	}
	want := []string{"ANTHROPIC_BASE_URL", "OP_SERVICE_ACCOUNT_TOKEN"}
	if !reflect.DeepEqual(tasks[0].EnvKeys, want) {
		t.Errorf("env_keys = %v, want %v", tasks[0].EnvKeys, want)
	}
}
