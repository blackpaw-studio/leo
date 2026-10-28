package config

import (
	"testing"
	"time"
)

func TestFollowRevisionTracksTheCacheDigest(t *testing.T) {
	allowProbes(t)
	dir := t.TempDir()
	leoBytes := followerHeader + "templates:\n  mine: {}\n"
	path := writeFile(t, dir, "leo.yaml", leoBytes)
	state := dir + "/state"

	_, noCache, err := LoadWithRevision(path)
	if err != nil {
		t.Fatal(err)
	}
	if noCache != RevisionOf([]byte(leoBytes)) {
		t.Fatal("without a cache the revision must stay leo.yaml's digest")
	}

	writeLeadCache(t, dir, "templates:\n  a: {model: opus}\n")
	_, first, err := LoadWithRevision(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == noCache {
		t.Fatal("revision ignores the lead layer")
	}
	if err := WriteFollowStatus(state, FollowStatus{LastAttempt: time.Now(), LastError: "boom", LastErrorCode: FollowUnreachable}); err != nil {
		t.Fatal(err)
	}
	if _, again, _ := LoadWithRevision(path); again != first {
		t.Fatal("a status write changed the revision")
	}
	// A pull with identical sections but a newer pulled_at keeps it too.
	fc, _ := ReadFollowCache(state)
	fc.PulledAt = fc.PulledAt.Add(time.Hour)
	if err := WriteFollowCache(state, *fc); err != nil {
		t.Fatal(err)
	}
	if _, again, _ := LoadWithRevision(path); again != first {
		t.Fatal("re-stamping the cache changed the revision")
	}
	writeLeadCache(t, dir, "templates:\n  a: {model: haiku}\n")
	if _, changed, _ := LoadWithRevision(path); changed == first {
		t.Fatal("a changed lead export kept the revision")
	}
}
