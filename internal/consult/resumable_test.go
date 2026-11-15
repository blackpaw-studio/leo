package consult

import (
	"encoding/json"
	"testing"
	"time"
)

func parkedRecord(harnessName string) Record {
	return Record{ID: "d-park", Kind: "dispatch", Mode: ModeInteractive, Harness: harnessName, Status: StatusClosed, Retained: true, SessionID: "sess-1"}
}

func TestResumablePredicate(t *testing.T) {
	cases := []struct {
		name       string
		rec        func() Record
		wantParked bool
		wantResume bool
	}{
		{"claude parked with a session", func() Record { return parkedRecord("claude") }, true, true},
		{"codex parked with a session", func() Record { return parkedRecord("codex") }, true, true},
		{"no session id", func() Record { r := parkedRecord("claude"); r.SessionID = ""; return r }, true, false},
		{"resume error recorded", func() Record {
			r := parkedRecord("claude")
			r.ResumeError = "harness started another session"
			return r
		}, true, false},
		{"released", func() Record {
			r := parkedRecord("claude")
			r.Status, r.Retained, r.ReleaseReason = StatusReleased, false, ReleaseReasonManual
			return r
		}, false, false},
		{"closed but not retained", func() Record { r := parkedRecord("claude"); r.Retained = false; return r }, false, false},
		{"opencode", func() Record { return parkedRecord("opencode") }, true, false},
		{"headless", func() Record { r := parkedRecord("claude"); r.Mode = ModeHeadless; return r }, false, false},
		{"pre-upgrade closed record", func() Record {
			var r Record
			raw := `{"id":"d-old","kind":"dispatch","mode":"interactive","harness":"claude","status":"closed","session_id":"sess-old","release_on_finish":false}`
			if err := json.Unmarshal([]byte(raw), &r); err != nil {
				t.Fatal(err)
			}
			return r
		}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := c.rec()
			if got := parked(rec); got != c.wantParked {
				t.Fatalf("parked = %v, want %v", got, c.wantParked)
			}
			if got := resumable(rec); got != c.wantResume {
				t.Fatalf("resumable = %v, want %v", got, c.wantResume)
			}
		})
	}
}

func TestRecordRetentionFieldsRoundTrip(t *testing.T) {
	rec := parkedRecord("claude")
	rec.ResumeError, rec.ReleaseReason = "x", ReleaseReasonRetention
	rec.Turns = []Turn{{TurnID: "d-park#1", ResultCollectedAt: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Retained || back.ResumeError != "x" || back.ReleaseReason != ReleaseReasonRetention || !back.Turns[0].ResultCollectedAt.Equal(rec.Turns[0].ResultCollectedAt) {
		t.Fatalf("round trip lost fields: %+v", back)
	}
}
