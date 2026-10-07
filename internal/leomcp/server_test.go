package leomcp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestResolveServerKeepsSymlinkPath pins the Homebrew choice: a leo started
// through a symlink (/opt/homebrew/bin/leo) keeps that path rather than the
// versioned target it resolves to, so agents launched before a `brew
// upgrade` still find a binary after the old version is removed.
func TestResolveServerKeepsSymlinkPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Caskroom", "leo", "1.0.0", "leo")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin", "leo")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	s := ResolveServer(func() (string, error) { return link, nil }, &warn)

	if want := []string{link, "mcp-server"}; !reflect.DeepEqual(s.Command(), want) {
		t.Errorf("Command() = %q, want %q", s.Command(), want)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning: %q", warn.String())
	}
}

func TestResolveServerFallsBackToBareLeoWithWarning(t *testing.T) {
	var warn bytes.Buffer
	s := ResolveServer(func() (string, error) { return "", errors.New("no exe") }, &warn)

	if want := []string{"leo", "mcp-server"}; !reflect.DeepEqual(s.Command(), want) {
		t.Errorf("Command() = %q, want %q", s.Command(), want)
	}
	if !strings.Contains(warn.String(), "warning") || !strings.Contains(warn.String(), "no exe") {
		t.Errorf("warning = %q, want it to name the failure", warn.String())
	}
}

func TestResolveServerFallsBackWhenPathIsMissing(t *testing.T) {
	var warn bytes.Buffer
	missing := filepath.Join(t.TempDir(), "gone", "leo")
	s := ResolveServer(func() (string, error) { return missing, nil }, &warn)

	if s.Executable() != FallbackBin {
		t.Errorf("Executable() = %q, want %q", s.Executable(), FallbackBin)
	}
	if !strings.Contains(warn.String(), "warning") {
		t.Errorf("warning = %q, want one", warn.String())
	}
}
