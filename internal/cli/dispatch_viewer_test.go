package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func viewerTestSetup(t *testing.T, show string) (*[][]string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(path, []byte("tasks: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldCfg, oldLocate, oldExe, oldExec := cfgFile, viewerLocateTmux, viewerExecutable, viewerExecCommandContext
	cfgFile = path
	viewerLocateTmux = func() (string, error) { return "/tmux", nil }
	viewerExecutable = func() (string, error) { return "/leo", nil }
	calls := new([][]string)
	viewerExecCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		*calls = append(*calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "show-options" {
			return exec.Command("sh", "-c", `printf %s "$1"`, "sh", show)
		}
		return exec.Command("true")
	}
	t.Cleanup(func() {
		cfgFile = oldCfg
		viewerLocateTmux = oldLocate
		viewerExecutable = oldExe
		viewerExecCommandContext = oldExec
	})
	return calls, path
}

func TestViewerMenuArgv(t *testing.T) {
	for _, placement := range []string{"pane", "window"} {
		for cap := 1; cap <= 6; cap++ {
			t.Run(fmt.Sprintf("%s-%d", placement, cap), func(t *testing.T) {
				calls, cfgPath := viewerTestSetup(t, fmt.Sprintf("@leo_viewer_placement %s\n@leo_viewer_max_panes %d\n", placement, cap))
				cmd := newViewerMenuCmd()
				cmd.SetArgs([]string{"--session", "demo"})
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				nextPlacement, from, to := "pane", "windows", "panes"
				if placement == "pane" {
					nextPlacement, from, to = "window", "panes", "windows"
				}
				nextCap := cap%6 + 1
				prefix := `run-shell "'/leo' --config '` + cfgPath + `' dispatch viewer `
				suffix := ` --session 'demo'"`
				want := [][]string{{"/tmux", "-L", "leo", "show-options", "-t", "=demo"}, {"/tmux", "-L", "leo", "display-menu", "-t", "=demo:", "-T", " leo · demo ", fmt.Sprintf("Viewer placement: %s  → %s", from, to), "p", prefix + "set placement=" + nextPlacement + suffix, fmt.Sprintf("Max panes: %d → %d", cap, nextCap), "m", prefix + fmt.Sprintf("set max_panes=%d", nextCap) + suffix, "", "Close finished viewers", "c", prefix + "close-finished" + suffix, "Save as default", "s", prefix + "save-default" + suffix}}
				if !reflect.DeepEqual(*calls, want) {
					t.Fatalf("calls=%q want=%q", *calls, want)
				}
			})
		}
	}
}
func TestViewerSetValidationAndArgv(t *testing.T) {
	calls, _ := viewerTestSetup(t, "")
	cmd := newViewerSetCmd()
	cmd.SetArgs([]string{"--session", "demo", "placement=window"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"/tmux", "-L", "leo", "set-option", "-t", "=demo", "@leo_viewer_placement", "window"}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("%q", *calls)
	}
}
func TestViewerErrorsDisplayMessage(t *testing.T) {
	calls, _ := viewerTestSetup(t, "")
	cmd := newViewerSetCmd()
	cmd.SetArgs([]string{"--session", "demo", "max_panes=7"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want error")
	}
	want := [][]string{{"/tmux", "-L", "leo", "display-message", "-t", "=demo:", "max_panes must be between 1 and 6"}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("%q", *calls)
	}
}
func TestViewerCommandsLocalOnly(t *testing.T) {
	calls, path := viewerTestSetup(t, "")
	if err := os.WriteFile(path, []byte("client:\n  default_host: remote\n  hosts:\n    remote:\n      ssh: ssh\n      hostname: example.com\ntasks: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newViewerSetCmd()
	cmd.SetArgs([]string{"--session", "demo", "max_panes=7"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("err=%v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("remote command touched tmux: %q", *calls)
	}
}
func TestSaveDefaultClearsOnlyAfterSuccess(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		saved, reloaded, wantClear bool
		wantErr                    string
	}{{"reload failed", true, false, false, "saved, reload failed"}, {"save failed", false, true, false, "saved, reload failed"}, {"success", true, true, true, ""}} {
		t.Run(tt.name, func(t *testing.T) {
			calls, path := viewerTestSetup(t, "@leo_viewer_placement window\n@leo_viewer_max_panes 4\n")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			state := filepath.Join(filepath.Dir(path), "state")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "api.token"), []byte("tok"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", port)), 0600); err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, call := range *calls {
					if len(call) > 4 && call[3] == "set-option" && call[4] == "-u" {
						t.Error("override cleared before save response")
					}
				}
				fmt.Fprintf(w, `{"ok":true,"data":{"saved":%t,"reloaded":%t}}`, tt.saved, tt.reloaded)
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			cmd := newViewerSaveDefaultCmd()
			cmd.SetArgs([]string{"--session", "demo"})
			runErr := cmd.Execute()
			if tt.wantErr != "" && (runErr == nil || !strings.Contains(runErr.Error(), tt.wantErr)) {
				t.Fatalf("err=%v want %q", runErr, tt.wantErr)
			}
			if tt.wantErr == "" && runErr != nil {
				t.Fatal(runErr)
			}
			clears := 0
			for _, call := range *calls {
				if len(call) > 4 && call[3] == "set-option" && call[4] == "-u" {
					clears++
				}
			}
			if (clears == 2) != tt.wantClear {
				t.Fatalf("clear calls=%d wantClear=%v; calls=%q", clears, tt.wantClear, *calls)
			}
		})
	}
}
func TestSaveDefaultClearFailure(t *testing.T) {
	calls, _ := viewerTestSetup(t, "")
	n := 0
	viewerExecCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		*calls = append(*calls, append([]string{name}, args...))
		n++
		if n >= 2 {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	if err := clearViewerOverrides(context.Background(), "/tmux", "demo"); err == nil {
		t.Fatal("want clear error")
	}
	if len(*calls) < 2 {
		t.Fatalf("calls=%q", *calls)
	}
}
