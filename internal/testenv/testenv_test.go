package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

func TestIsolatePointsHomeAtTempDirAndClearsOverrides(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/real/claude")
	t.Setenv("CODEX_HOME", "/real/codex")
	before, _ := os.LookupEnv("HOME")

	restore, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	if home == before || !strings.HasPrefix(home, os.TempDir()) {
		t.Fatalf("HOME = %q, want a fresh dir under %q", home, os.TempDir())
	}
	for _, k := range homeOverrides {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s = %q, want unset", k, v)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "probe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	restore()
	if got := os.Getenv("HOME"); got != before {
		t.Errorf("HOME after restore = %q, want %q", got, before)
	}
	if got := os.Getenv("CODEX_HOME"); got != "/real/codex" {
		t.Errorf("CODEX_HOME after restore = %q", got)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("isolated home not removed: %v", err)
	}
}
