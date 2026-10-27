package web

import (
	"net/http"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/follow"
)

func TestConfigFollowStatusRoute(t *testing.T) {
	h := newFollowerAPIHarness(t, nil)
	res := h.do(http.MethodGet, "/config/follow", "", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Status, res.Raw)
	}
	d := res.data()
	if d["following"] != true || d["host"] != "lead" || d["lead_revision"] != "sha256:leadrev" {
		t.Fatalf("data = %v", d)
	}
	invalid, _ := d["invalid"].(map[string]any)
	if tmpl, _ := invalid["template"].(map[string]any); tmpl["bad"] == nil {
		t.Fatalf("invalid = %v", invalid)
	}
}

func TestConfigFollowSyncRoute(t *testing.T) {
	syncer := &fakeSyncer{out: follow.Outcome{Changed: true}}
	h := newFollowerAPIHarness(t, syncer)
	res := h.do(http.MethodPost, "/config/follow/sync", "", nil)
	if res.Status != http.StatusOK || syncer.calls != 1 || res.data()["changed"] != true {
		t.Fatalf("sync = %d %s (calls %d)", res.Status, res.Raw, syncer.calls)
	}
	if st, _ := res.data()["status"].(map[string]any); st["host"] != "lead" {
		t.Fatalf("status = %v", res.data())
	}

	syncer.err = &config.FollowError{Code: config.FollowUnreachable, Detail: "refused"}
	res = h.do(http.MethodPost, "/config/follow/sync", "", nil)
	if res.Status != http.StatusBadGateway || res.Body["code"] != "unreachable" {
		t.Fatalf("failed sync = %d %s", res.Status, res.Raw)
	}
}

func TestConfigFollowSyncWithoutLoopOrFollow(t *testing.T) {
	h := newFollowerAPIHarness(t, nil)
	if res := h.do(http.MethodPost, "/config/follow/sync", "", nil); res.Status != http.StatusServiceUnavailable {
		t.Fatalf("no loop: %d %s", res.Status, res.Raw)
	}
	plain := newAPIHarness(t)
	plain.svc.follower = func() FollowSyncer { return &fakeSyncer{err: follow.ErrNotFollowing} }
	res := plain.do(http.MethodPost, "/config/follow/sync", "", nil)
	if res.Status != http.StatusConflict || res.Body["code"] != CodeNotFollowing {
		t.Fatalf("not following: %d %s", res.Status, res.Raw)
	}
	if res := plain.do(http.MethodGet, "/config/follow", "", nil); res.Status != http.StatusOK || res.data()["following"] != false {
		t.Fatalf("GET on a non-follower: %d %s", res.Status, res.Raw)
	}
}
