package config

import (
	"path/filepath"
	"runtime"
	"testing"
)

// exampleConfigPath resolves examples/leo.yaml relative to this file, so the
// test works regardless of the caller's working directory.
func exampleConfigPath(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file path via runtime.Caller")
	}
	// this file lives at <repo>/internal/config/example_test.go
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(repoRoot, "examples", "leo.yaml")
}

func TestExampleConfigLoadsAndValidates(t *testing.T) {
	// Arrange
	path := exampleConfigPath(t)

	// Act
	cfg, err := Load(path)

	// Assert
	if err != nil {
		t.Fatalf("Load(%q) returned error: %v", path, err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("examples/leo.yaml failed Validate(): %v", err)
	}
}
