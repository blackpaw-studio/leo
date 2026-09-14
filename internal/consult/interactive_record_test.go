package consult

import (
	"bufio"
	"os"
	"testing"
	"time"
)

func TestInteractiveRecordOrdering(t *testing.T) {
	r, dir := newTestRecorder(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return now }
	h, err := r.Open(Record{ID: "d-interactive", Mode: ModeInteractive, Status: StatusQueued, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	ih, ok := h.(interactiveRecordHandle)
	if !ok {
		t.Fatal("file handle lacks interactive persistence")
	}
	rec := Record{ID: "d-interactive", Mode: ModeInteractive, Status: StatusRunning, StartedAt: now, Turns: []Turn{{TurnID: "d-interactive#1", Source: TurnSourceOrchestrator}}}
	if err := ih.SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	if err := ih.AppendEvent("turn", rec.Turns[0]); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir + "/d-interactive.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !bufio.NewScanner(f).Scan() {
		t.Fatal("no event")
	}
	loaded, err := readRecord(dir + "/d-interactive.json")
	if err != nil || loaded.Status != StatusRunning {
		t.Fatalf("record=%+v err=%v", loaded, err)
	}
}
