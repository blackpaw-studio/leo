package web

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/follow"
)

// followerHeaderYAML makes a leo.yaml follow the host "lead".
const followerHeaderYAML = "client:\n  default_host: localhost\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\nfollow:\n  host: lead\n"

// followerLeadSections is the cached lead config the follower tests use.
const followerLeadSections = `"templates":{"lt":{"model":"opus"},"bad":{"harness":"nope"}},` +
	`"environments":{"le":{"A":"lead"}},` +
	`"delegation":{"active_profile":"day","profiles":{"day":{"roles":{"implement":"lt"}},"night":{"roles":{}}}},` +
	`"defaults":{"max_turns":9}`

// writeFollowerHome writes a follower leo.yaml (header + local) over a cached
// lead export of sections and returns the config path. The D4 probes pass.
func writeFollowerHome(t *testing.T, sections, local string) string {
	t.Helper()
	t.Cleanup(config.SetFollowProbes(config.FollowProbes{
		LookPath: func(n string) (string, error) { return "/bin/" + n, nil },
		Stat:     func(string) (fs.FileInfo, error) { return nil, nil },
	}))
	dir := t.TempDir()
	export := `{"schema":1,"kind":"leo.follow.export","leo_version":"v0.64.0","home_id":"ab","revision":"sha256:leadrev",` + sections + `}`
	exp, err := config.ParseFollowExport([]byte(export))
	if err != nil {
		t.Fatal(err)
	}
	err = config.WriteFollowCache(filepath.Join(dir, "state"), config.FollowCache{
		Host: "lead", SSH: "evan@10.0.2.10", PulledAt: time.Now(), LeadRevision: exp.Revision, LeadVersion: exp.LeoVersion,
		Digest: exp.Digest(), Export: []byte(export),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(path, []byte(followerHeaderYAML+local), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newFollowerAPIHarness(t *testing.T, syncer FollowSyncer) *apiHarness {
	t.Helper()
	path := writeFollowerHome(t, followerLeadSections, "templates:\n  mine: {model: haiku}\nenvironments:\n  me: {B: local}\n")
	svc := NewConfigService(ConfigServiceOptions{ConfigPath: path, Follower: func() FollowSyncer { return syncer }})
	return newAPIHarnessOver(t, svc, path, "")
}

// fakeSyncer answers SyncNow with a fixed outcome.
type fakeSyncer struct {
	out   follow.Outcome
	err   error
	calls int
}

func (f *fakeSyncer) SyncNow(context.Context) (follow.Outcome, error) {
	f.calls++
	return f.out, f.err
}

// liveRevision is the config revision as Load computes it, the lead cache
// included (revisionOnDisk hashes leo.yaml alone).
func (h *apiHarness) liveRevision() string {
	h.t.Helper()
	_, rev, err := config.LoadWithRevision(h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	return rev
}
