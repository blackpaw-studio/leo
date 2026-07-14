package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
)

// stubStdinIsTerminal forces stdinIsTerminal() to the given value for the
// duration of the test. Tests run from CI or a pipe without this override
// would bail out of the picker with "stdin is not a terminal" before we can
// exercise the interesting branches.
func stubStdinIsTerminal(t *testing.T, v bool) {
	t.Helper()
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return v }
	t.Cleanup(func() { stdinIsTerminal = old })
}

func TestRunAttachPickerRejectsNonTTY(t *testing.T) {
	stubStdinIsTerminal(t, false)
	cfg := &config.Config{HomePath: t.TempDir()}
	err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{})
	if err == nil || !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("want non-TTY error, got %v", err)
	}
}

func TestRunAttachPickerFailsFastWhenDaemonDown(t *testing.T) {
	stubStdinIsTerminal(t, true)

	oldList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return nil, fmt.Errorf("connecting to daemon: dial unix: connect: no such file or directory")
	}
	t.Cleanup(func() { agentListFn = oldList })

	cfg := &config.Config{HomePath: t.TempDir()}
	err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{})
	if err == nil || !strings.Contains(err.Error(), "leo service") {
		t.Fatalf("want daemon-down fail-fast error mentioning leo service, got %v", err)
	}
}
