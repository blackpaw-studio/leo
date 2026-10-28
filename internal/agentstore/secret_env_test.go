package agentstore

import (
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

func TestSaveNeverPersistsSecretValues(t *testing.T) {
	home := t.TempDir()
	ref := secretenv.Ref{Backend: secretenv.Keychain, Service: "OP"}
	rec := Record{
		Name:       "a",
		Env:        map[string]string{"PLAIN": "p", "OP_TOKEN": "S3CR3T-SENTINEL"},
		EnvSecrets: map[string]secretenv.Ref{"OP_TOKEN": ref},
	}
	if err := Save(home, rec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(FilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "S3CR3T-SENTINEL") {
		t.Fatalf("agents.json holds the secret value:\n%s", data)
	}
	if !strings.Contains(string(data), `"env_secrets"`) {
		t.Fatalf("agents.json lacks env_secrets:\n%s", data)
	}
	got, err := Load(FilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	back := got["a"]
	if _, has := back.Env["OP_TOKEN"]; has || back.Env["PLAIN"] != "p" {
		t.Errorf("env = %v, want PLAIN only", back.Env)
	}
	if back.EnvSecrets["OP_TOKEN"].Service != "OP" {
		t.Errorf("env_secrets = %v", back.EnvSecrets)
	}
	// The caller's record is untouched.
	if rec.Env["OP_TOKEN"] != "S3CR3T-SENTINEL" {
		t.Error("Save mutated the caller's env")
	}
}
