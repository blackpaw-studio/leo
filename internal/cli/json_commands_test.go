package cli

import (
	"bytes"
	"encoding/json"
	"strings"
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
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	err := executeRoot(cmd, append([]string{"--config", path}, args...))
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

// Every failure of a --json command is exactly one JSON object on stdout,
// including the ones cobra raises before the command's own code runs.
func TestJSONCommandsReportArgAndFlagErrorsAsInvalidArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"handover without a direction", []string{"service", "handover", "--json"}},
		{"handover --to-app without a label", []string{"service", "handover", "--to-app", "--json"}},
		{"web enable with a bad drain timeout", []string{"web", "enable", "--json", "--drain-timeout", "0s"}},
		{"unknown flag", []string{"pair", "--json", "--bogus"}},
		{"unknown flag before --json", []string{"pair", "--bogus", "--json"}},
		{"unexpected argument", []string{"pair", "--json", "extra"}},
		{"mutually exclusive flags", []string{"service", "restart", "--json", "--graceful", "--no-graceful"}},
		{"invalid flag value", []string{"service", "restart", "--json", "--drain-timeout", "soon"}},
		{"handover with --json=true", []string{"service", "handover", "--json=true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useTempConfig(t, nil)

			stdout, err := runRoot(t, tt.args...)

			if ExitCode(err) != 1 || err.Error() != "" {
				t.Fatalf("err = %v, want a silent exit 1", err)
			}
			var got jsonFailure
			dec := json.NewDecoder(strings.NewReader(stdout))
			if derr := dec.Decode(&got); derr != nil {
				t.Fatalf("stdout is not a JSON object: %q", stdout)
			}
			if dec.More() || strings.Count(stdout, "\n") != 1 {
				t.Fatalf("stdout must be exactly one object, got %q", stdout)
			}
			if got.Error != "invalid_args" || got.Message == "" {
				t.Fatalf("failure = %+v", got)
			}
		})
	}
}

func TestUnknownFlagMessageIsCobrasOwn(t *testing.T) {
	useTempConfig(t, nil)

	stdout, _ := runRoot(t, "pair", "--json", "--bogus")

	if !strings.Contains(stdout, "unknown flag: --bogus") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestTextModeErrorsStayPlain(t *testing.T) {
	useTempConfig(t, nil)

	stdout, err := runRoot(t, "pair", "--bogus")

	if stdout != "" || err == nil || err.Error() == "" {
		t.Fatalf("stdout %q err %v: without --json errors stay plain", stdout, err)
	}
}

func TestJSONFalseDoesNotSwitchOnJSONErrors(t *testing.T) {
	useTempConfig(t, nil)

	stdout, err := runRoot(t, "pair", "--json=false", "--bogus")

	if stdout != "" || err == nil || err.Error() == "" {
		t.Fatalf("stdout %q err %v", stdout, err)
	}
}

func TestOtherCommandsAreNotWrapped(t *testing.T) {
	useTempConfig(t, nil)

	stdout, err := runRoot(t, "version", "--bogus", "--json")

	if stdout != "" || err == nil || err.Error() == "" {
		t.Fatalf("stdout %q err %v: only commands that promise JSON get it", stdout, err)
	}
}
