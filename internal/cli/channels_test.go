package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTelegramTokenFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "abc:123")
	tok, src, err := resolveTelegramToken()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tok != "abc:123" {
		t.Errorf("token = %q", tok)
	}
	if !strings.Contains(src, "env") {
		t.Errorf("source = %q", src)
	}
}

func TestResolveTelegramTokenFromDotenv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	dir := t.TempDir()
	envFile := filepath.Join(dir, "telegram.env")
	if err := os.WriteFile(envFile, []byte("OTHER=val\nTELEGRAM_BOT_TOKEN=\"file:token\"\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	prev := telegramTokenLookupPaths
	telegramTokenLookupPaths = []string{envFile}
	t.Cleanup(func() { telegramTokenLookupPaths = prev })

	tok, src, err := resolveTelegramToken()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tok != "file:token" {
		t.Errorf("token = %q", tok)
	}
	if src != envFile {
		t.Errorf("source = %q want %q", src, envFile)
	}
}

func TestResolveTelegramTokenMissing(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	prev := telegramTokenLookupPaths
	telegramTokenLookupPaths = []string{"/nonexistent/path/.env"}
	t.Cleanup(func() { telegramTokenLookupPaths = prev })

	if _, _, err := resolveTelegramToken(); err == nil {
		t.Error("expected error when no token can be resolved")
	}
}

func TestRegisterTelegramCommandsHappyPath(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test:token")

	// Track all calls — the new code hits getMyCommands + setMyCommands per scope.
	type call struct{ Method, Path, Body string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, string(body)})

		if strings.Contains(r.URL.Path, "getMyCommands") {
			// Simulate plugin having /start and /help at this scope.
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"command":"start","description":"Welcome"},{"command":"help","description":"Help"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer srv.Close()

	prev := telegramAPIBase
	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = prev })

	out := &bytes.Buffer{}
	if err := registerTelegramCommands(out); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Should have 3 scopes × (1 get + 1 set) = 6 calls.
	gets, sets := 0, 0
	for _, c := range calls {
		if strings.Contains(c.Path, "getMyCommands") {
			gets++
		}
		if strings.Contains(c.Path, "setMyCommands") {
			sets++
			// Verify merged result contains both plugin and Leo commands.
			var sent struct {
				Commands []map[string]string `json:"commands"`
			}
			if err := json.Unmarshal([]byte(c.Body), &sent); err != nil {
				t.Fatalf("decode setMyCommands body: %v", err)
			}
			names := map[string]bool{}
			for _, cmd := range sent.Commands {
				names[cmd["command"]] = true
			}
			// Plugin's existing commands preserved.
			if !names["start"] || !names["help"] {
				t.Errorf("missing plugin commands in merged set: %v", names)
			}
			// Leo commands present.
			if !names["clear"] || !names["compact"] || !names["stop"] {
				t.Errorf("missing Leo commands in merged set: %v", names)
			}
		}
	}
	if gets != 3 {
		t.Errorf("expected 3 getMyCommands calls, got %d", gets)
	}
	if sets != 3 {
		t.Errorf("expected 3 setMyCommands calls, got %d", sets)
	}
}

func TestMergeCommandsPreservesExistingAndAddsLeo(t *testing.T) {
	existing := []telegramCommand{
		{Command: "start", Description: "Welcome"},
		{Command: "help", Description: "Help"},
		{Command: "clear", Description: "Old clear desc"},
	}
	leo := []telegramCommand{
		{Command: "clear", Description: "New clear desc"},
		{Command: "stop", Description: "Interrupt"},
	}
	merged := mergeCommands(existing, leo)
	names := map[string]string{}
	for _, c := range merged {
		names[c.Command] = c.Description
	}
	if names["start"] != "Welcome" {
		t.Error("plugin command 'start' should be preserved")
	}
	if names["help"] != "Help" {
		t.Error("plugin command 'help' should be preserved")
	}
	if names["clear"] != "New clear desc" {
		t.Errorf("Leo should override 'clear'; got %q", names["clear"])
	}
	if names["stop"] != "Interrupt" {
		t.Error("Leo command 'stop' should be added")
	}
	if len(merged) != 4 {
		t.Errorf("expected 4 merged commands, got %d", len(merged))
	}
}

func TestRegisterTelegramCommandsAPIError(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test:token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer srv.Close()

	prev := telegramAPIBase
	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = prev })

	err := registerTelegramCommands(&bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error on API rejection")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error should surface telegram description; got %v", err)
	}
}

func TestChannelsCmdRejectsUnsupportedType(t *testing.T) {
	cmd := newChannelsRegisterCommandsCmd()
	cmd.SetArgs([]string{"discord"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for unsupported channel type")
	}
}
