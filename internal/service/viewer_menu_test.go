package service

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestViewerMenuBindingExecutableFailure(t *testing.T) {
	orig := serviceExecutable
	t.Cleanup(func() { serviceExecutable = orig })
	serviceExecutable = func() (string, error) { return "", errors.New("no executable") }
	var warnings bytes.Buffer
	if got := resolveViewerMenuBindings("/cfg", &warnings); got != nil {
		t.Fatalf("bindings=%v", got)
	}
	if !strings.Contains(warnings.String(), "binding skipped") || !strings.Contains(warnings.String(), "no executable") {
		t.Fatalf("warning=%q", warnings.String())
	}
}
