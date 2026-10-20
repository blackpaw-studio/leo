package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/managed"
)

// runRoot runs argv through the real root command (usage and error output
// silenced, as in production) and returns stdout and the error. The root
// command resets --config to its default, so the temp config must be passed
// explicitly or the run would read the real ~/.leo.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	path := cfgFile // newRootCmd resets cfgFile, so read it first
	if path == "" {
		t.Fatal("call useTempConfig first: runRoot must never fall back to the real leo home")
	}
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"--config", path}, args...))
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return stdout.String(), err
}

func TestPairJSONCmdWebDisabledIsExactlyTheCodedObject(t *testing.T) {
	useTempConfig(t, nil)

	stdout, err := runRoot(t, "pair", "--json")

	if stdout != "{\"error\":\"web_disabled\"}\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d", ExitCode(err))
	}
}

func TestWebEnableJSONCmdWritesTheConfigAndReportsIt(t *testing.T) {
	useTempConfig(t, nil)
	stubManaged(t, managed.State{})

	stdout, err := runRoot(t, "web", "enable", "--json")

	if err != nil {
		t.Fatal(err)
	}
	const want = "{\"ok\":true,\"changed\":true,\"restarted\":false,\"web\":{\"enabled\":true,\"bind\":\"127.0.0.1\",\"port\":8370}}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	cfg, err := config.Load(cfgFile)
	if err != nil || !cfg.Web.Enabled {
		t.Fatalf("config not written: %v %+v", err, cfg)
	}
}

func TestHandoverJSONCmdFailureIsOneObjectWithTheFullResult(t *testing.T) {
	useTempConfig(t, nil)
	stubManaged(t, managed.State{}) // --to-cli on an unmanaged install fails in preflight

	stdout, err := runRoot(t, "service", "handover", "--to-cli", "--json")

	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d", ExitCode(err))
	}
	var got struct {
		OK    bool   `json:"ok"`
		Step  string `json:"step"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("stdout is not one JSON object: %q", stdout)
	}
	if got.OK || got.Step != "preflight" || got.Error == "" {
		t.Fatalf("result = %+v (stdout %q)", got, stdout)
	}
}
