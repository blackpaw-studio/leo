package service

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestViewerMenuBindingExecutableFailure(t *testing.T) {
	exe := func() (string, error) { return "", errors.New("no executable") }
	var warnings bytes.Buffer
	if got := resolveViewerMenuBindings(exe, "/cfg", &warnings); got != nil {
		t.Fatalf("bindings=%v", got)
	}
	if !strings.Contains(warnings.String(), "binding skipped") || !strings.Contains(warnings.String(), "no executable") {
		t.Fatalf("warning=%q", warnings.String())
	}
}

// The prefix+L binding persists in the tmux server, so it must carry the
// injected (stable) leo path rather than whichever binary the daemon runs.
func TestViewerMenuBindingUsesInjectedExecutable(t *testing.T) {
	exe := func() (string, error) { return "/app/Application Support/Leo/bin/leo", nil }
	got := resolveViewerMenuBindings(exe, "/cfg/leo.yaml", &bytes.Buffer{})
	want := []tmux.ViewerMenuBinding{{LeoPath: "/app/Application Support/Leo/bin/leo", ConfigPath: "/cfg/leo.yaml"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bindings = %+v, want %+v", got, want)
	}
}
