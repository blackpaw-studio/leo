package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/attachprefs"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/picker"
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

// stubPickerRun forces pickerRunFn to a fake for the duration of the test,
// capturing the backends map it was invoked with so tests can assert on
// which hosts were included without driving the real TUI.
func stubPickerRun(t *testing.T) *map[string]picker.Backend {
	t.Helper()
	var captured map[string]picker.Backend
	old := pickerRunFn
	pickerRunFn = func(ctx context.Context, backends map[string]picker.Backend, _ picker.Gates, _ picker.Options) (picker.Result, error) {
		captured = backends
		return picker.Result{}, nil
	}
	t.Cleanup(func() { pickerRunFn = old })
	return &captured
}

func TestRunAttachPickerSkipsLocalWhenDaemonDownButHostsConfigured(t *testing.T) {
	stubStdinIsTerminal(t, true)

	oldList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return nil, fmt.Errorf("connecting to daemon: dial unix: connect: no such file or directory")
	}
	t.Cleanup(func() { agentListFn = oldList })

	captured := stubPickerRun(t)

	cfg := &config.Config{
		HomePath: t.TempDir(),
		Client: config.ClientConfig{
			Hosts: map[string]config.HostConfig{
				"dionysus": {SSH: "leo@dionysus.example.com"},
			},
		},
	}
	err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{})
	if err != nil {
		t.Fatalf("want no error when remote hosts are configured, got %v", err)
	}

	backends := *captured
	if _, ok := backends[picker.LocalHost]; ok {
		t.Errorf("backends = %v, want no local backend when the daemon probe failed", backends)
	}
	if _, ok := backends["dionysus"]; !ok {
		t.Errorf("backends = %v, want dionysus SSH backend present", backends)
	}
}

func TestRunAttachPickerIncludesLocalWhenDaemonUp(t *testing.T) {
	stubStdinIsTerminal(t, true)

	oldList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return nil, nil
	}
	t.Cleanup(func() { agentListFn = oldList })

	captured := stubPickerRun(t)

	cfg := &config.Config{
		HomePath: t.TempDir(),
		Client: config.ClientConfig{
			Hosts: map[string]config.HostConfig{
				"dionysus": {SSH: "leo@dionysus.example.com"},
			},
		},
	}
	err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{})
	if err != nil {
		t.Fatalf("want no error when the daemon probe succeeds, got %v", err)
	}

	backends := *captured
	if _, ok := backends[picker.LocalHost]; !ok {
		t.Errorf("backends = %v, want local backend present when the daemon probe succeeded", backends)
	}
	if _, ok := backends["dionysus"]; !ok {
		t.Errorf("backends = %v, want dionysus SSH backend present", backends)
	}
}

func TestRunAttachPickerSavesReturnedSortMode(t *testing.T) {
	stubStdinIsTerminal(t, true)
	oldList := agentListFn
	agentListFn = func(context.Context, string) ([]agent.Record, error) { return nil, nil }
	t.Cleanup(func() { agentListFn = oldList })
	oldRun := pickerRunFn
	pickerRunFn = func(context.Context, map[string]picker.Backend, picker.Gates, picker.Options) (picker.Result, error) {
		return picker.Result{SortMode: picker.SortModeName}, nil
	}
	t.Cleanup(func() { pickerRunFn = oldRun })
	cfg := &config.Config{HomePath: t.TempDir()}
	if err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{}); err != nil {
		t.Fatalf("runAttachPicker: %v", err)
	}
	if got := attachprefs.Load(attachPrefsPath(cfg.HomePath)).Sort; got != attachprefs.SortName {
		t.Fatalf("sort = %q, want name", got)
	}
}

func TestStampLastAttached(t *testing.T) {
	home := t.TempDir()
	at := time.Date(2026, 9, 5, 23, 10, 0, 0, time.UTC)
	stampLastAttached(home, "remote", "vitals", at)
	if got := attachprefs.Load(attachPrefsPath(home)).LastAttached["remote/vitals"]; !got.Equal(at) {
		t.Fatalf("stamp = %v, want %v", got, at)
	}
}
