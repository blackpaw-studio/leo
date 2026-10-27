package follow

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestBuildReport(t *testing.T) {
	t.Cleanup(config.SetFollowProbes(config.FollowProbes{
		LookPath: func(n string) (string, error) { return "/bin/" + n, nil },
		Stat:     func(string) (fs.FileInfo, error) { return nil, nil },
	}))
	home := t.TempDir()
	state := filepath.Join(home, "state")
	path := filepath.Join(home, "leo.yaml")
	writeYAML(t, path, "client:\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\nfollow:\n  host: lead\n  interval: 1m\n")

	cfg, _ := config.Load(path)
	r := BuildReport(cfg, time.Unix(1000, 0))
	if !r.Following || r.Host != "lead" || r.PulledAt != nil || len(r.Warnings) == 0 {
		t.Fatalf("no-cache report = %+v", r)
	}

	export := `{"schema":1,"kind":"leo.follow.export","leo_version":"v0.64.0","revision":"sha256:rev","templates":{"bad":{"harness":"nope"},"ok":{}}}`
	exp, _ := config.ParseFollowExport([]byte(export))
	pulled := time.Unix(1000, 0).UTC()
	if err := config.WriteFollowCache(state, config.FollowCache{Host: "lead", PulledAt: pulled, LeadRevision: exp.Revision, LeadVersion: exp.LeoVersion, Digest: exp.Digest(), Export: []byte(export)}); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFollowStatus(state, config.FollowStatus{LastAttempt: pulled.Add(time.Minute), LastError: "unreachable: refused", LastErrorCode: config.FollowUnreachable}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(path)
	fresh := BuildReport(cfg, pulled.Add(3*time.Minute))
	if fresh.Stale || fresh.LeadRevision != "sha256:rev" || fresh.LeadVersion != "v0.64.0" || fresh.LastErrorCode != "unreachable" || fresh.AgeSeconds != 180 {
		t.Fatalf("fresh report = %+v", fresh)
	}
	if fresh.Invalid["template"]["bad"] == "" {
		t.Fatalf("invalid = %v", fresh.Invalid)
	}
	if stale := BuildReport(cfg, pulled.Add(3*time.Minute+time.Second)); !stale.Stale {
		t.Fatal("3 × interval + 1s must be stale")
	}

	if err := os.WriteFile(path, []byte("templates: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(path)
	if r := BuildReport(cfg, time.Now()); r.Following || r.Host != "" {
		t.Fatalf("non-follower report = %+v", r)
	}
}

func TestReportMessages(t *testing.T) {
	pulled := time.Unix(1000, 0).UTC()
	r := Report{
		Following: true, Host: "lead", Interval: "1m0s", PulledAt: &pulled, AgeSeconds: 600, Stale: true,
		LeadVersion: "v0.70.0", LastError: "unreachable: refused", LastErrorCode: "unreachable",
		Warnings: []string{"no cache yet"},
		Invalid: map[string]map[string]string{
			"template": {"b": "no binary", "a": "bad harness"},
			"defaults": {"model": "unknown model"},
		},
	}
	want := []string{
		"no cache yet",
		`the cache of lead "lead" is stale: last pulled 10m0s ago (interval 1m0s)`,
		`the last pull from "lead" failed: unreachable: refused`,
		`lead "lead" runs leo v0.70.0, newer than this host's v0.64.0: settings only it knows are ignored here`,
		`defaults key model from lead "lead" is ignored on this host: unknown model`,
		`template "a" from lead "lead" is invalid on this host: bad harness`,
		`template "b" from lead "lead" is invalid on this host: no binary`,
	}
	got := r.Messages("v0.64.0")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Messages =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, local := range []string{"dev", "", "v0.70.0", "v0.71.0"} {
		for _, m := range r.Messages(local) {
			if strings.Contains(m, "newer than") {
				t.Errorf("local %q: unexpected version warning %q", local, m)
			}
		}
	}
	if m := (Report{}).Messages("v1.0.0"); len(m) != 0 {
		t.Fatalf("non-follower messages = %v", m)
	}
}
