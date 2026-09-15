package consult

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestFileRecorderResumeAppendsWithSequenceContinuity(t *testing.T) {
	stateDir := t.TempDir()
	r := NewFileRecorder(stateDir)
	rec := Record{ID: "d-resume", StartedAt: time.Now(), Status: StatusRunning}
	h, err := r.Open(rec)
	if err != nil {
		t.Fatal(err)
	}
	events := h.(interface{ AppendEvent(string, any) error })
	if err := events.AppendEvent("turn", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatal(err)
	}
	rec.Status = StatusQueued
	h, err = r.Resume(rec)
	if err != nil {
		t.Fatal(err)
	}
	events = h.(interface{ AppendEvent(string, any) error })
	if err := events.AppendEvent("turn", map[string]int{"n": 2}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(StreamPath(stateDir, rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"seq":1`) || !strings.Contains(s, `"seq":2`) {
		t.Fatalf("stream lacks continuous sequence: %s", s)
	}
}
