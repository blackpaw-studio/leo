package redact

import (
	"reflect"
	"testing"
)

func TestIsSecretKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"OP_SERVICE_ACCOUNT_TOKEN", true},
		{"ANTHROPIC_AUTH_TOKEN", true},
		{"ANTHROPIC_API_KEY", true},
		{"AWS_ACCESS_KEY_ID", true},
		{"AWS_REGION", true}, // whole AWS_ namespace is treated as sensitive
		{"OP_CONNECT_HOST", true},
		{"GITHUB_PASSWORD", true},
		{"DB_PASSWD", true},
		{"SOME_CREDENTIAL", true},
		{"SESSION_COOKIE", true},
		{"PRIVATE_KEY", true},
		{"op_service_account_token", true}, // case-insensitive
		{"APIKeyName", true},               // mixed case, KEY embedded mid-word
		{"ANTHROPIC_BASE_URL", false},
		{"BLACKPAW_TELEGRAM_RECEIVE", false},
		{"PATH", false},
		{"LEO_CHANNELS", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsSecretKey(tt.key); got != tt.want {
			t.Errorf("IsSecretKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestValue(t *testing.T) {
	if got := Value("OP_SERVICE_ACCOUNT_TOKEN", "ops_reallylongsecret"); got != Mask {
		t.Errorf("Value(secret) = %q, want %q", got, Mask)
	}
	if got := Value("ANTHROPIC_BASE_URL", "http://localhost:3325"); got != "http://localhost:3325" {
		t.Errorf("Value(non-secret) = %q, want the value unchanged", got)
	}
	if got := Value("OP_SERVICE_ACCOUNT_TOKEN", ""); got != "" {
		t.Errorf("Value(secret, empty) = %q, want empty (nothing to leak)", got)
	}
}

func TestEnvMapMasksSecretsAndCopies(t *testing.T) {
	original := map[string]string{
		"OP_SERVICE_ACCOUNT_TOKEN": "ops_reallylongsecret",
		"ANTHROPIC_BASE_URL":       "http://localhost:3325",
	}
	got := EnvMap(original)

	want := map[string]string{
		"OP_SERVICE_ACCOUNT_TOKEN": Mask,
		"ANTHROPIC_BASE_URL":       "http://localhost:3325",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnvMap() = %v, want %v", got, want)
	}
	if original["OP_SERVICE_ACCOUNT_TOKEN"] != "ops_reallylongsecret" {
		t.Error("EnvMap mutated its input; it must return a copy")
	}
}

func TestEnvMapNil(t *testing.T) {
	if got := EnvMap(nil); got != nil {
		t.Errorf("EnvMap(nil) = %v, want nil", got)
	}
}

func TestKeys(t *testing.T) {
	got := Keys(map[string]string{"ZZZ": "1", "AAA": "2", "MMM": "3"})
	want := []string{"AAA", "MMM", "ZZZ"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Keys() = %v, want %v (sorted)", got, want)
	}
	if got := Keys(nil); got != nil {
		t.Errorf("Keys(nil) = %v, want nil", got)
	}
}
