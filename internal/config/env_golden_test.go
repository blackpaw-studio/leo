package config

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateEnvGolden = flag.Bool("update-env-golden", false, "rewrite testdata/env_roundtrip.*.golden")

// TestEnvRoundTripGolden pins how a ref-free config's env values save and
// encode: the golden files were written by the map[string]string types, so
// EnvValue must reproduce them byte for byte.
func TestEnvRoundTripGolden(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "env_roundtrip.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "leo.yaml")
	if err := Save(out, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// HomePath is the checkout's absolute testdata dir, not part of what the
	// golden pins.
	cfg.HomePath = ""
	var js bytes.Buffer
	enc := json.NewEncoder(&js)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		t.Fatalf("json: %v", err)
	}
	for name, got := range map[string][]byte{"yaml": saved, "json": js.Bytes()} {
		golden := filepath.Join("testdata", "env_roundtrip."+name+".golden")
		if *updateEnvGolden {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s output differs from %s:\n--- got\n%s\n--- want\n%s", name, golden, got, want)
		}
	}
}
