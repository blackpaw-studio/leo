package follow

import (
	"io/fs"
	"os"
	"path/filepath"
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
