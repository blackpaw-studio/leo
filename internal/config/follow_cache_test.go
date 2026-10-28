package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleCache(host string) FollowCache {
	return FollowCache{
		Host: host, SSH: "evan@10.0.2.10",
		PulledAt:     time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		LeadRevision: "sha256:aa", LeadVersion: "v0.64.0", LeadHomeID: "abcd",
		Digest: "sha256:bb",
		Export: []byte(`{"schema":1,"kind":"leo.follow.export"}`),
	}
}

func TestFollowCacheRoundTrip(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if got, err := ReadFollowCache(state); err != nil || got != nil {
		t.Fatalf("missing cache = %v, %v; want nil, nil", got, err)
	}
	want := sampleCache("dionysus")
	if err := WriteFollowCache(state, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFollowCache(state)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != want.Host || !got.PulledAt.Equal(want.PulledAt) || got.Digest != want.Digest || string(got.Export) != string(want.Export) || got.Schema != followCacheSchema {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestFollowCacheWriteIsPrivate(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, followCacheFile)
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil { //nolint:gosec // the test wants a broad mode
		t.Fatal(err)
	}
	if err := WriteFollowCache(state, sampleCache("a")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestFollowCacheFailedWriteKeepsOld(t *testing.T) {
	state := t.TempDir()
	if err := WriteFollowCache(state, sampleCache("old")); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(state, followCacheFile))
	orig := writeTemp
	writeTemp = func(f *os.File, _ []byte) error { _ = f.Close(); return errors.New("disk full") }
	t.Cleanup(func() { writeTemp = orig })
	if err := WriteFollowCache(state, sampleCache("new")); err == nil {
		t.Fatal("write succeeded through a failing writeTemp")
	}
	if after := readFile(t, filepath.Join(state, followCacheFile)); after != before {
		t.Fatalf("cache changed after a failed write:\n%s", after)
	}
}

func TestFollowCacheStale(t *testing.T) {
	c := sampleCache("a")
	interval := time.Minute
	if c.Stale(c.PulledAt.Add(3*interval), interval) {
		t.Fatal("exactly 3 × interval must not be stale")
	}
	if !c.Stale(c.PulledAt.Add(3*interval+time.Second), interval) {
		t.Fatal("3 × interval + 1s must be stale")
	}
}

func TestFollowStatusRoundTrip(t *testing.T) {
	state := t.TempDir()
	if got, err := ReadFollowStatus(state); err != nil || got != (FollowStatus{}) {
		t.Fatalf("missing status = %+v, %v", got, err)
	}
	want := FollowStatus{LastAttempt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), LastError: "ssh: connect refused", LastErrorCode: FollowUnreachable}
	if err := WriteFollowStatus(state, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFollowStatus(state)
	if err != nil || !got.LastAttempt.Equal(want.LastAttempt) || got.LastError != want.LastError || got.LastErrorCode != want.LastErrorCode {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}
