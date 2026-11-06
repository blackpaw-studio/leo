package agentmove

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/session"
)

// removeTranscript deletes the agent's resume transcript, as Claude Code's
// 30-day cleanup does.
func (f *moveFixture) removeTranscript(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.claude, "projects", session.ProjectSlug(f.ws), "sid-1.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// A missing transcript is refused when the request is made: no job is
// created, nothing stops and nothing reaches the destination, and the
// message says what to do about it.
func TestJobsStartRefusesMissingTranscriptBeforeAnyJob(t *testing.T) {
	for _, mode := range []string{ModeMove, ModeCopy} {
		t.Run(mode, func(t *testing.T) {
			f := newMoveFixture(t)
			f.removeTranscript(t)
			jobs := NewJobs(f.mv)
			_, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: mode, To: "laptop"})
			if !HasCode(err, CodeSessionMissing) {
				t.Fatalf("Start = %v, want session_missing", err)
			}
			for _, want := range []string{"no longer exists", "fresh session"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q lacks %q", err, want)
				}
			}
			if len(jobs.order) != 0 || len(jobs.active) != 0 {
				t.Fatalf("a job was created: order=%v active=%v", jobs.order, jobs.active)
			}
			if f.log.has("stop ") || f.log.has("verb ") {
				t.Fatalf("something ran before the refusal: %v", f.log.all())
			}
		})
	}
}

// The in-job check stays as a backstop and speaks the same plain sentence.
func TestMissingTranscriptJobErrorIsReadable(t *testing.T) {
	f := newMoveFixture(t)
	f.removeTranscript(t)
	_, err := f.mv.Run(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop"}, nil)
	if !HasCode(err, CodeSessionMissing) || !strings.Contains(err.Error(), "no longer exists") || !strings.Contains(err.Error(), "fresh session") {
		t.Fatalf("err = %v, want the plain session_missing sentence", err)
	}
}

// fresh_session skips the check and sends no session: the manifest the
// destination commits from names none, and the sent record carries none.
func TestFreshSessionMoveSendsNoSession(t *testing.T) {
	f := newMoveFixture(t)
	f.removeTranscript(t)
	jobs := NewJobs(f.mv)
	st, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop", FreshSession: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st = waitJob(t, jobs, st.ID); st.State != JobDone {
		t.Fatalf("job = %+v", st)
	}
	if len(f.ep.manifests) == 0 {
		t.Fatal("no manifest reached the destination")
	}
	for i, m := range f.ep.manifests {
		if m.SourceSessionID != "" || m.Agent.SessionID != "" || !m.FreshSession {
			t.Fatalf("manifest %d = source %q, record session %q, fresh %v; want no session and fresh", i, m.SourceSessionID, m.Agent.SessionID, m.FreshSession)
		}
	}
}

// A fresh copy starts its own conversation: no fork source, no pinned fork
// id, and no transcript snapshot is staged (claude's snapshot needs one).
func TestFreshSessionCopyDoesNotFork(t *testing.T) {
	f := newMoveFixture(t)
	f.removeTranscript(t)
	req := f.copyReq("", "")
	req.FreshSession = true
	if _, err := f.copy(t, req); err != nil {
		t.Fatalf("copy: %v", err)
	}
	for i, m := range f.ep.manifests {
		if m.SourceSessionID != "" || m.Copy == nil || m.Copy.SessionID != "" || !m.FreshSession {
			t.Fatalf("manifest %d = source %q, copy %+v, fresh %v; want no session ids", i, m.SourceSessionID, m.Copy, m.FreshSession)
		}
	}
	if _, err := os.Stat(filepath.Join(StagingDir(f.home, "web"), "harness")); err != nil {
		t.Fatalf("staging: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(StagingDir(f.home, "web"), "harness"))
	if len(entries) != 0 {
		t.Fatalf("a transcript snapshot was staged: %v", entries)
	}
}

// Even when the transcript exists, fresh_session ignores it: the agent's
// session is not what moves.
func TestFreshSessionIgnoresAnExistingTranscript(t *testing.T) {
	f := newMoveFixture(t)
	f.rec.SessionID = "sid-1"
	jobs := NewJobs(f.mv)
	st, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop", FreshSession: true})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, jobs, st.ID)
	for i, m := range f.ep.manifests {
		if m.SourceSessionID != "" {
			t.Fatalf("manifest %d names session %q", i, m.SourceSessionID)
		}
	}
}
