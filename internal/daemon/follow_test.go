package daemon

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/blackpaw-studio/leo/internal/follow"
)

type fakeFollower struct{ kicks atomic.Int32 }

func (f *fakeFollower) SyncNow(context.Context) (follow.Outcome, error) { return follow.Outcome{}, nil }
func (f *fakeFollower) Kick()                                           { f.kicks.Add(1) }

// A reload re-reads follow settings, so turning follow on or changing the
// lead host takes effect without a restart.
func TestReloadConfigKicksTheFollower(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	saveClients(t, path, nil)
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	f := &fakeFollower{}
	s.SetFollower(f)
	if err := s.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	if got := f.kicks.Load(); got != 1 {
		t.Fatalf("kicks = %d, want 1", got)
	}
}
