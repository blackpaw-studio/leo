//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/follow"
)

// followLeadE2E is a lead leo home (no daemon: the export is a plain CLI
// run over ssh) and a supervised follower daemon reaching it through
// testdata/fakessh.
type followLeadE2E struct {
	*interactiveE2E
	lead     string // the lead's leo home
	sshLog   string
	sshFail  string // the toggle file that makes fakessh fail
	cliEnv   []string
	follower string // the follower's leo.yaml
}

const leadYAMLTemplate = `templates_file: %[1]s/shared.yaml
defaults:
  max_turns: 7
environments:
  lead-env:
    LEAD_MARK: one
templates:
  lt:
    harness: codex
    model: gpt-5
    workspace: %[1]s
    env:
      CODEX_HOME: %[1]s
      FAKECLAUDE_DISPATCH_INTERACTIVE: "1"
delegation:
  active_profile: day
  profiles:
    day:
      roles: {}
`

const leadSharedYAML = "templates:\n  shared-t:\n    model: opus\n"

// leadTemplate is a lead template the follower can spawn: codex under the
// fake binary, like the fixture's local templates.
func leadTemplate(name, home string) string {
	return fmt.Sprintf("  %s:\n    harness: codex\n    model: gpt-5\n    workspace: %s\n    env:\n      CODEX_HOME: %s\n      FAKECLAUDE_DISPATCH_INTERACTIVE: \"1\"\n", name, home, home)
}

func newFollowLeadE2E(t *testing.T) *followLeadE2E {
	t.Helper()
	lead := mkTempE2EDir(t, "leo-e2e-lead-*")
	writeE2EFile(t, filepath.Join(lead, "leo.yaml"), fmt.Sprintf(leadYAMLTemplate, lead))
	writeE2EFile(t, filepath.Join(lead, "shared.yaml"), leadSharedYAML)

	bin := mkTempE2EDir(t, "leo-e2e-sshbin-*")
	shim, err := os.ReadFile(filepath.Join(findRepoRoot(), "e2e", "testdata", "fakessh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), shim, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &followLeadE2E{lead: lead, sshLog: filepath.Join(bin, "ssh.log"), sshFail: filepath.Join(bin, "fail")}
	env := []string{
		"PATH=" + bin + ":" + filepath.Dir(fakeclaude) + ":" + os.Getenv("PATH"),
		"FAKESSH_HOME=" + lead, "FAKESSH_LOG=" + f.sshLog, "FAKESSH_FAIL=" + f.sshFail,
	}
	f.cliEnv = env
	f.interactiveE2E = newInteractiveE2EWithOptions(t, interactiveOptions{
		daemonEnv: env,
		extraConfig: func(string) string {
			return fmt.Sprintf("client:\n  hosts:\n    lead:\n      ssh: lead-fake\n      leo_path: %s\nfollow:\n  host: lead\n", leoBin)
		},
	})
	f.follower = f.cfgPath
	return f
}

func writeE2EFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// leo runs the CLI against the follower and fails the test on a non-zero
// exit unless wantFail.
func (f *followLeadE2E) leo(t *testing.T, wantFail bool, args ...string) (string, string) {
	t.Helper()
	stdout, stderr, code := runLeo(t, f.ws, f.cliEnv, append([]string{"-c", f.follower}, args...)...)
	if (code != 0) != wantFail {
		t.Fatalf("leo %v: exit %d\nstdout:\n%s\nstderr:\n%s\nservice output:\n%s", args, code, stdout, stderr, f.output.String())
	}
	return stdout, stderr
}

func (f *followLeadE2E) sync(t *testing.T) syncJSON {
	t.Helper()
	out, _ := f.leo(t, false, "sync", "--json")
	var res syncJSON
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("sync --json: %v\n%s", err, out)
	}
	return res
}

type syncJSON struct {
	Changed bool          `json:"changed"`
	Status  follow.Report `json:"status"`
}

func (f *followLeadE2E) templateSources(t *testing.T) map[string]string {
	t.Helper()
	out, _ := f.leo(t, false, "template", "list", "--json")
	var entries []struct{ Name, Source, Invalid string }
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("template list --json: %v\n%s", err, out)
	}
	sources := map[string]string{}
	for _, e := range entries {
		sources[e.Name] = e.Source
	}
	return sources
}

func TestFollowLead(t *testing.T) {
	f := newFollowLeadE2E(t)

	// The daemon pulled at start; a sync joins or repeats that pull.
	f.sync(t)
	sources := f.templateSources(t)
	for name, want := range map[string]string{"lt": "lead", "shared-t": "lead", "caller": "local"} {
		if sources[name] != want {
			t.Fatalf("template %s source = %q, want %q (all: %v)", name, sources[name], want, sources)
		}
	}

	// A lead edit reaches the follower on the next sync, which reloads it;
	// an unchanged sync does not.
	leadYAML := filepath.Join(f.lead, "leo.yaml")
	edited := strings.Replace(fmt.Sprintf(leadYAMLTemplate, f.lead), "templates:\n", "templates:\n"+leadTemplate("lt2", f.lead), 1)
	writeE2EFile(t, leadYAML, edited)
	if res := f.sync(t); !res.Changed {
		t.Fatalf("sync after a lead edit reported no change: %+v", res)
	}
	if res := f.sync(t); res.Changed {
		t.Fatalf("a repeated sync reported a change: %+v", res)
	}
	if got := f.templateSources(t)["lt2"]; got != "lead" {
		t.Fatalf("lt2 source = %q after the edit", got)
	}

	// An override is local and survives the next sync.
	f.leo(t, false, "template", "override", "lt")
	f.sync(t)
	if got := f.templateSources(t)["lt"]; got != "local-override-lead" {
		t.Fatalf("lt source after override + sync = %q", got)
	}

	// Unreachable lead: sync fails and says why, the cache still serves.
	writeE2EFile(t, f.sshFail, "")
	_, stderr := f.leo(t, true, "sync")
	if !strings.Contains(stderr, "unreachable") {
		t.Fatalf("failed sync stderr:\n%s", stderr)
	}
	if out, _ := f.leo(t, false, "sync", "--status"); !strings.Contains(out, "unreachable") {
		t.Fatalf("sync --status after a failed pull:\n%s", out)
	}
	if out, _ := f.leo(t, false, "template", "show", "lt2", "--resolved"); !strings.Contains(out, "gpt-5") {
		t.Fatalf("template show lt2 from the cache:\n%s", out)
	}
	var spawned struct {
		Name string `json:"name"`
	}
	f.request(t, http.MethodPost, "/api/agent/spawn", map[string]string{"template": "lt2", "name": "from-lead", "prompt": "stay alive"}, &spawned)
	if spawned.Name == "" {
		t.Fatal("spawn from a cached lead template returned no name")
	}
	if err := os.Remove(f.sshFail); err != nil {
		t.Fatal(err)
	}

	// A lead whose leo predates `config export --follow`.
	stub := filepath.Join(f.lead, "old-leo")
	writeE2EFile(t, stub, "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'leo v0.1.0'; exit 0; fi\necho 'Error: unknown command \"export\" for \"leo config\"' >&2\nexit 1\n")
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	follower, err := os.ReadFile(f.follower)
	if err != nil {
		t.Fatal(err)
	}
	writeE2EFile(t, f.follower, strings.Replace(string(follower), "leo_path: "+leoBin, "leo_path: "+stub, 1))
	if _, stderr := f.leo(t, true, "sync"); !strings.Contains(stderr, "lead_too_old: v0.1.0") {
		t.Fatalf("sync against an old lead:\n%s", stderr)
	}

	// Every ssh call ran in batch mode with the leo path quoted for the
	// remote shell.
	log, err := os.ReadFile(f.sshLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BatchMode=yes", "'" + leoBin + "'", "'" + stub + "'", "export", "--follow"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("ssh argv log lacks %q:\n%s", want, log)
		}
	}
}
