package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/follow"
)

const cliFollowerHeader = "client:\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\nfollow:\n  host: lead\n  interval: 1m\n"

const cliLeadSections = `"templates":{"lt":{"model":"opus"},"bad":{"harness":"nope"}},` +
	`"environments":{"le":{"A":"lead"}},` +
	`"delegation":{"active_profile":"day","profiles":{"day":{"roles":{}},"night":{"roles":{}}}}`

var cliPulledAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func cliLeadExport(sections string) string {
	return `{"schema":1,"kind":"leo.follow.export","leo_version":"v0.64.0","home_id":"leadhome","revision":"sha256:leadrev",` + sections + `}`
}

// newFollowerCLIConfig writes a follower leo.yaml (header + local) and, when
// sections is not empty, a cache pulled at cliPulledAt; cfgFile points at it.
func newFollowerCLIConfig(t *testing.T, sections, local string) string {
	t.Helper()
	return newFollowerCLIConfigIn(t, t.TempDir(), sections, local)
}

func newFollowerCLIConfigIn(t *testing.T, home, sections, local string) string {
	t.Helper()
	t.Cleanup(config.SetFollowProbes(config.FollowProbes{
		LookPath: func(n string) (string, error) { return "/bin/" + n, nil },
		Stat:     func(string) (fs.FileInfo, error) { return nil, nil },
	}))
	if sections != "" {
		export := cliLeadExport(sections)
		exp, err := config.ParseFollowExport([]byte(export))
		if err != nil {
			t.Fatal(err)
		}
		err = config.WriteFollowCache(filepath.Join(home, "state"), config.FollowCache{
			Host: "lead", SSH: "evan@10.0.2.10", PulledAt: cliPulledAt, LeadRevision: exp.Revision,
			LeadVersion: exp.LeoVersion, Digest: exp.Digest(), Export: []byte(export),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(path, []byte(cliFollowerHeader+local), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// runCLI runs leo args against cfgFile (newRootCmd resets cfgFile through
// its --config flag, so it is passed back explicitly).
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	path := cfgFile
	cmd := newRootCmd()
	args = append([]string{"--config", path}, args...)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestTemplateRemoveLeadEntryIsRefused(t *testing.T) {
	path := newFollowerCLIConfig(t, cliLeadSections, "templates:\n  mine: {model: haiku}\n")
	before := readFile(t, path)
	_, err := runCLI(t, "template", "remove", "lt", "--yes")
	var owned *config.OwnedByLeadError
	if !errors.As(err, &owned) || owned.Name != "lt" || owned.Op != "delete" {
		t.Fatalf("err = %v, want an owned_by_lead refusal", err)
	}
	if !strings.Contains(err.Error(), "leo template override lt") {
		t.Fatalf("err = %v, want the override hint", err)
	}
	if after := readFile(t, path); after != before {
		t.Fatalf("leo.yaml changed:\n%s", after)
	}
}

func TestOverrideCommandsWriteTheLeadEntry(t *testing.T) {
	cases := []struct {
		args []string
		kind config.EntryKind
		name string
	}{
		{[]string{"template", "override", "lt"}, config.KindTemplate, "lt"},
		{[]string{"config", "override", "template", "lt"}, config.KindTemplate, "lt"},
		{[]string{"config", "override", "environment", "le"}, config.KindEnvironment, "le"},
		{[]string{"config", "override", "profile", "night"}, config.KindProfile, "night"},
		{[]string{"delegation", "override", "night"}, config.KindProfile, "night"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			path := newFollowerCLIConfig(t, cliLeadSections, "")
			if out, err := runCLI(t, tc.args...); err != nil {
				t.Fatalf("%v: %v\n%s", tc.args, err, out)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.EntrySource(tc.kind, tc.name); got != config.SourceLocalOverrideLead {
				t.Fatalf("source = %q after override; leo.yaml:\n%s", got, readFile(t, path))
			}
		})
	}
}

func TestOverrideRefusesAnInvalidOrUnknownEntry(t *testing.T) {
	path := newFollowerCLIConfig(t, cliLeadSections, "")
	before := readFile(t, path)
	for _, name := range []string{"bad", "nope"} {
		if _, err := runCLI(t, "template", "override", name); err == nil {
			t.Errorf("override %s: want an error", name)
		}
	}
	if readFile(t, path) != before {
		t.Fatal("leo.yaml changed")
	}
}

func TestTemplateListShowsFollowSources(t *testing.T) {
	newFollowerCLIConfig(t, cliLeadSections, "templates:\n  mine: {model: haiku}\n")
	got := map[string]templateListEntry{}
	for _, e := range runListJSON(t) {
		got[e.Name] = e
	}
	if got["lt"].Source != "lead" || got["mine"].Source != "local" {
		t.Fatalf("sources = %+v", got)
	}
	if got["bad"].Source != "lead" || got["bad"].Invalid == "" {
		t.Fatalf("invalid lead template = %+v", got["bad"])
	}

	table := captureStdout(t, func() {
		cmd := newTemplateListCmd()
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Error(err)
		}
	})
	for _, want := range []string{"SOURCE", "lead · invalid", "local"} {
		if !strings.Contains(table, want) {
			t.Fatalf("table lacks %q:\n%s", want, table)
		}
	}
}

func withSyncClock(t *testing.T, now time.Time) {
	t.Helper()
	old := syncNow
	syncNow = func() time.Time { return now }
	t.Cleanup(func() { syncNow = old })
}

func TestSyncStatusShowsStale(t *testing.T) {
	newFollowerCLIConfig(t, cliLeadSections, "")
	withSyncClock(t, cliPulledAt.Add(2*time.Minute))
	out, err := runCLI(t, "sync", "--status")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "stale") || !strings.Contains(out, "sha256:leadrev") || !strings.Contains(out, "2m0s ago") {
		t.Fatalf("fresh status:\n%s", out)
	}

	withSyncClock(t, cliPulledAt.Add(3*time.Minute+time.Second))
	out, err = runCLI(t, "sync", "--status", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var r follow.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil || !r.Stale || r.Host != "lead" {
		t.Fatalf("json status = %+v (%v)\n%s", r, err, out)
	}
}

type cliFakeRunner struct {
	args   [][]string
	stdout string
}

func (f *cliFakeRunner) Run(_ context.Context, name string, args []string) ([]byte, []byte, error) {
	f.args = append(f.args, append([]string{name}, args...))
	return []byte(f.stdout), nil, nil
}

func TestSyncWithoutDaemonPullsInProcess(t *testing.T) {
	path := newFollowerCLIConfig(t, "", "")
	runner := &cliFakeRunner{stdout: cliLeadExport(`"templates":{"lt":{}}`)}
	old := syncRunner
	syncRunner = runner
	t.Cleanup(func() { syncRunner = old })
	withSyncClock(t, cliPulledAt)

	out, err := runCLI(t, "sync")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	state := filepath.Join(filepath.Dir(path), "state")
	want := [][]string{{"ssh", "-o", "BatchMode=yes", "evan@10.0.2.10",
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(state, "remotes", "lead.ctl"),
		"-o", "ConnectTimeout=10", `"$HOME"/'.local/bin/leo'`, "config", "export", "--follow"}}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("argv:\n got %q\nwant %q", runner.args, want)
	}
	fc, err := config.ReadFollowCache(state)
	if err != nil || fc == nil || !fc.PulledAt.Equal(cliPulledAt) {
		t.Fatalf("cache = %+v (%v)", fc, err)
	}
	if !strings.Contains(out, "sha256:leadrev") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestSyncFallsBackOnlyWhenTheDaemonCannotSync(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantPull bool
	}{
		{"daemon without a follow loop", http.StatusServiceUnavailable, `{"ok":false,"code":"follow_unavailable","error":"no loop"}`, true},
		{"daemon pull failed", http.StatusBadGateway, `{"ok":false,"code":"unreachable","error":"ssh failed"}`, false},
		{"daemon answer unreadable", http.StatusOK, `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A short home keeps the socket path under the sun_path limit.
			home, err := os.MkdirTemp("", "leo-sy-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			newFollowerCLIConfigIn(t, home, "", "")
			runner := &cliFakeRunner{stdout: cliLeadExport(`"templates":{"lt":{}}`)}
			old := syncRunner
			syncRunner = runner
			t.Cleanup(func() { syncRunner = old })
			startStubDaemonSocket(t, home, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			out, err := runCLI(t, "sync")
			if pulled := len(runner.args) > 0; pulled != tc.wantPull {
				t.Fatalf("in-process pull = %v, want %v (err %v)\n%s", pulled, tc.wantPull, err, out)
			}
			if !tc.wantPull && err == nil {
				t.Fatalf("want the daemon's failure reported\n%s", out)
			}
		})
	}
}

func TestSyncRefusesANonFollower(t *testing.T) {
	newTestConfigWithTemplates(t)
	if _, err := runCLI(t, "sync"); !errors.Is(err, follow.ErrNotFollowing) {
		t.Fatalf("err = %v", err)
	}
}

func TestDelegationUseOnAFollower(t *testing.T) {
	path := newFollowerCLIConfig(t, cliLeadSections, "")
	if _, _, err := delegationCommand(t, "use", "night"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "active_profile: night") || strings.Contains(got, "day:") {
		t.Fatalf("leo.yaml:\n%s", got)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Delegation.ActiveProfile != "night" {
		t.Fatalf("reloaded = %+v (%v)", cfg.Delegation, err)
	}
}

func TestFollowWarningsInValidateAndStartup(t *testing.T) {
	newFollowerCLIConfig(t, "", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range configWarningFindings(cfg) {
		found = found || f.Check == "follow"
	}
	if !found {
		t.Fatalf("validate findings lack a follow warning: %+v", configWarningFindings(cfg))
	}
	if w := strings.Join(startupWarnings(cfg), "\n"); !strings.Contains(w, `"lead"`) {
		t.Fatalf("startup warnings = %q", w)
	}
}
