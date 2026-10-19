package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testClock is a settable clock for the store and service.
// The reminder loop reads Now from its own goroutine, so access is locked.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustInsert(t *testing.T, st *Store, m Message) Message {
	t.Helper()
	if err := st.InsertMessage(m); err != nil {
		t.Fatalf("InsertMessage(%s): %v", m.ID, err)
	}
	return m
}

func msg(ids *ULIDSource, clock *testClock, agent, dir, kind, text string) Message {
	clock.Advance(time.Second)
	return Message{ID: ids.New(), Agent: agent, Direction: dir, Kind: kind, Text: text, TS: clock.Now()}
}

func TestStorePagesNewestFirstWithBeforeAndLimit(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	var all []Message
	for i := 0; i < 5; i++ {
		all = append(all, mustInsert(t, st, msg(ids, clock, "assistant", DirectionIn, KindText, string(rune('a'+i)))))
	}
	mustInsert(t, st, msg(ids, clock, "other", DirectionIn, KindText, "elsewhere"))

	page, more, err := st.Messages("assistant", Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Text != "e" || page[1].Text != "d" || !more {
		t.Fatalf("first page = %+v (more %v); want e, d and more", texts(page), more)
	}
	page, more, err = st.Messages("assistant", Page{Limit: 2, Before: page[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(page); len(got) != 2 || got[0] != "c" || got[1] != "b" || !more {
		t.Fatalf("second page = %v (more %v); want c, b and more", got, more)
	}
	last, more, _ := st.Messages("assistant", Page{Limit: 2, Before: page[1].ID})
	if len(last) != 1 || more {
		t.Fatalf("last page = %v (more %v); want a alone", texts(last), more)
	}
	if !page[0].TS.Equal(all[2].TS) {
		t.Fatalf("ts round-trip: got %v want %v", page[0].TS, all[2].TS)
	}
}

func TestStoreClampsPageLimit(t *testing.T) {
	if got := (Page{Limit: 5000}).limit(); got != MaxPageLimit {
		t.Fatalf("limit 5000 clamps to %d, got %d", MaxPageLimit, got)
	}
	if got := (Page{}).limit(); got != DefaultPageLimit {
		t.Fatalf("zero limit defaults to %d, got %d", DefaultPageLimit, got)
	}
}

func TestStoreFiltersByTask(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	card := msg(ids, clock, "assistant", DirectionOut, KindTask, "report")
	card.Task, card.RunID = "daily-news", "run-1"
	mustInsert(t, st, card)
	mustInsert(t, st, msg(ids, clock, "assistant", DirectionOut, KindText, "chatter"))

	page, _, err := st.Messages("assistant", Page{Task: "daily-news"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].RunID != "run-1" {
		t.Fatalf("task filter = %+v", page)
	}
}

func TestStoreThreadsReportLastMessageUnreadAndPin(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	mustInsert(t, st, msg(ids, clock, "assistant", DirectionIn, KindText, "hi"))
	first := mustInsert(t, st, msg(ids, clock, "assistant", DirectionOut, KindText, "hello"))
	mustInsert(t, st, msg(ids, clock, "assistant", DirectionOut, KindText, "anything else?"))
	mustInsert(t, st, msg(ids, clock, "web", DirectionOut, KindText, "built"))

	threads, err := st.Threads()
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %+v", threads)
	}
	byAgent := map[string]Thread{}
	for _, th := range threads {
		byAgent[th.Agent] = th
	}
	a := byAgent["assistant"]
	if a.LastMessage.Text != "anything else?" || a.Unread != 2 || !a.Pinned {
		t.Fatalf("assistant thread = %+v", a)
	}
	if byAgent["web"].Pinned {
		t.Fatal("only assistant is pinned")
	}
	if threads[0].Agent != "web" {
		t.Fatalf("threads sort by latest message first, got %s", threads[0].Agent)
	}

	if err := st.MarkRead("assistant", first.ID); err != nil {
		t.Fatal(err)
	}
	threads, _ = st.Threads()
	for _, th := range threads {
		if th.Agent == "assistant" && th.Unread != 1 {
			t.Fatalf("unread after marking the first reply read = %d, want 1", th.Unread)
		}
	}
	// Marking an older id never moves the marker backwards.
	if err := st.MarkRead("assistant", "00000000000000000000000000"); err != nil {
		t.Fatal(err)
	}
	threads, _ = st.Threads()
	for _, th := range threads {
		if th.Agent == "assistant" && th.Unread != 1 {
			t.Fatalf("read marker moved backwards: unread = %d", th.Unread)
		}
	}
}

func TestStoreTransitionIsCompareAndSet(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	m := msg(ids, clock, "assistant", DirectionOut, KindAsk, "pick")
	m.State = StateOpen
	mustInsert(t, st, m)

	ok, err := st.Transition(m.ID, []string{StateOpen}, StateAnswered, &Answer{Values: []string{"yes"}})
	if err != nil || !ok {
		t.Fatalf("first transition = %v, %v", ok, err)
	}
	ok, err = st.Transition(m.ID, []string{StateOpen}, StateExpired, nil)
	if err != nil || ok {
		t.Fatalf("second transition must lose: %v, %v", ok, err)
	}
	got, err := st.Message(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateAnswered || got.Answer == nil || got.Answer.Values[0] != "yes" {
		t.Fatalf("state = %s/%s", got.State, got.Answer)
	}
}

func TestStoreAttachmentsJoinMessagesAndPrune(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	att := Attachment{ID: ids.New(), Agent: "assistant", Filename: "a.png", MIME: "image/png", Bytes: 3, Path: "/x/a.png", Created: clock.Now()}
	if err := st.InsertAttachment(att); err != nil {
		t.Fatal(err)
	}
	m := msg(ids, clock, "assistant", DirectionIn, KindFile, "")
	if err := st.InsertMessageWith(m, nil, []string{att.ID}); err != nil {
		t.Fatal(err)
	}
	again := msg(ids, clock, "assistant", DirectionIn, KindFile, "")
	if err := st.InsertMessageWith(again, nil, []string{att.ID}); !errors.Is(err, ErrUnusable) {
		t.Fatalf("an attachment links to one message only: %v", err)
	}
	if _, err := st.Message(again.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a message whose link failed must not be stored: %v", err)
	}
	page, _, _ := st.Messages("assistant", Page{})
	if len(page) != 1 || len(page[0].Attachments) != 1 || !page[0].Attachments[0].Available {
		t.Fatalf("attachments not joined: %+v", page)
	}

	clock.Advance(91 * 24 * time.Hour)
	pruned, err := st.PruneAttachments(clock.Now().Add(-AttachmentRetention))
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || pruned[0] != "/x/a.png" {
		t.Fatalf("pruned = %v", pruned)
	}
	got, _ := st.Attachment(att.ID)
	if got.Path != "" || got.Available {
		t.Fatalf("pruned attachment keeps its row with path cleared: %+v", got)
	}
}

func TestStoreLinkClaimsAnUnassignedUpload(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	att := Attachment{ID: ids.New(), Filename: "a.txt", MIME: "text/plain", Path: "/x", Created: clock.Now()}
	_ = st.InsertAttachment(att)
	m := msg(ids, clock, "assistant", DirectionIn, KindFile, "")
	if err := st.InsertMessageWith(m, nil, []string{att.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Attachment(att.ID); got.Agent != "assistant" || got.MessageID != m.ID {
		t.Fatalf("claimed attachment = %+v", got)
	}
}

func TestStoreLinkRefusesAnotherAgentsAttachment(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	att := Attachment{ID: ids.New(), Agent: "web", Filename: "a.txt", MIME: "text/plain", Path: "/x", Created: clock.Now()}
	_ = st.InsertAttachment(att)
	if err := st.InsertMessageWith(msg(ids, clock, "assistant", DirectionIn, KindFile, ""), nil, []string{att.ID}); !errors.Is(err, ErrUnusable) {
		t.Fatalf("linking another agent's upload must fail: %v", err)
	}
}

func TestStoreCapturesRoundTrip(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	c := Capture{MessageID: "01M4RMAZ800000000000000001", Agent: "assistant", Prompt: "[Leo Chat] hi", DeliveredAt: clock.Now()}
	if err := st.AddCapture(c); err != nil {
		t.Fatal(err)
	}
	got, err := st.Captures("assistant")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Prompt != c.Prompt || !got[0].DeliveredAt.Equal(c.DeliveredAt) {
		t.Fatalf("captures = %+v", got)
	}
	if removed, err := st.RemoveCapture(c.MessageID); err != nil || !removed {
		t.Fatalf("first remove = %v, %v", removed, err)
	}
	if removed, _ := st.RemoveCapture(c.MessageID); removed {
		t.Fatal("a second remove must report the capture already gone")
	}
	if got, _ := st.Captures("assistant"); len(got) != 0 {
		t.Fatalf("capture not removed: %+v", got)
	}
}

func TestStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	m := msg(ids, clock, "assistant", DirectionIn, KindText, "persist me")
	if err := st.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	st, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	got, err := st.Message(m.ID)
	if err != nil || got.Text != "persist me" {
		t.Fatalf("reopened message = %+v, %v", got, err)
	}
}

func texts(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

// assertPrivate fails unless every one of the store's files at path is 0600.
func assertPrivate(t *testing.T, path string) {
	t.Helper()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
		}
	}
}

func TestStoreCreatesItsFilesPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	clock := newTestClock()
	mustInsert(t, st, msg(NewULIDSource(clock.Now, nil), clock, "assistant", DirectionIn, KindText, "hi"))
	assertPrivate(t, path)
}

func TestStoreRepairsReadableSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	clock := newTestClock()
	mustInsert(t, st, msg(NewULIDSource(clock.Now, nil), clock, "assistant", DirectionIn, KindText, "hi"))
	_ = st.Close()
	// An earlier build left the database and its sidecars group- and
	// world-readable.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	assertPrivate(t, path)
}

func sched(id, agent string, fireAt time.Time) Schedule {
	return Schedule{ID: id, Agent: agent, Kind: KindText, Text: "text " + id, FireAt: fireAt, Created: fireAt.Add(-time.Hour)}
}

func TestStoreSchedulesListOnlyPending(t *testing.T) {
	st := openTestStore(t)
	base := newTestClock().Now()
	for _, s := range []Schedule{
		sched("C", "assistant", base.Add(3*time.Hour)),
		sched("A", "assistant", base.Add(time.Hour)),
		sched("B", "assistant", base.Add(time.Hour)),
		sched("X", "other", base.Add(30*time.Minute)),
		sched("F", "assistant", base.Add(10*time.Minute)),
		sched("K", "assistant", base.Add(20*time.Minute)),
	} {
		if err := st.InsertSchedule(s); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := st.FireSchedule("F", Message{ID: "M1", Agent: "assistant", Direction: DirectionOut, Kind: KindText, Text: "x", TS: base}, base); err != nil || !ok {
		t.Fatalf("fire F: %v %v", ok, err)
	}
	if err := st.CancelSchedule("K", "assistant", base); err != nil {
		t.Fatal(err)
	}

	got, err := st.PendingSchedules("assistant")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if want := []string{"A", "B", "C"}; len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("pending = %v, want %v", ids, want)
	}
	if !got[0].FireAt.Equal(base.Add(time.Hour)) || got[0].Text != "text A" || got[0].Kind != KindText {
		t.Fatalf("round-trip = %+v", got[0])
	}

	next, ok, err := st.NextPending()
	if err != nil || !ok || next.ID != "X" {
		t.Fatalf("NextPending = %+v %v %v, want X", next, ok, err)
	}
	due, err := st.DuePending(base.Add(time.Hour))
	if err != nil || len(due) != 3 || due[0].ID != "X" || due[1].ID != "A" || due[2].ID != "B" {
		t.Fatalf("DuePending = %+v %v", due, err)
	}
}

func TestStoreFireScheduleIsOnceAndAtomic(t *testing.T) {
	st := openTestStore(t)
	base := newTestClock().Now()
	for _, id := range []string{"A", "B"} {
		if err := st.InsertSchedule(sched(id, "assistant", base)); err != nil {
			t.Fatal(err)
		}
	}
	m := func(id string) Message {
		return Message{ID: id, Agent: "assistant", Direction: DirectionOut, Kind: KindText, Text: "x", TS: base}
	}
	if ok, err := st.FireSchedule("A", m("M1"), base); err != nil || !ok {
		t.Fatalf("first fire = %v %v", ok, err)
	}
	if ok, err := st.FireSchedule("A", m("M2"), base); err != nil || ok {
		t.Fatalf("second fire = %v %v, want false", ok, err)
	}
	if err := st.CancelSchedule("B", "assistant", base); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FireSchedule("B", m("M3"), base); err != nil || ok {
		t.Fatalf("cancelled fire = %v %v, want false", ok, err)
	}
	msgs, _, _ := st.Messages("assistant", Page{})
	if len(msgs) != 1 || msgs[0].ID != "M1" {
		t.Fatalf("messages = %+v, want only M1", texts(msgs))
	}
}

// A failed message insert rolls the fired mark back with it: the reminder
// stays pending and a later fire posts it exactly once.
func TestStoreFireScheduleRollsBackWhenTheInsertFails(t *testing.T) {
	st := openTestStore(t)
	base := newTestClock().Now()
	if err := st.InsertSchedule(sched("A", "assistant", base)); err != nil {
		t.Fatal(err)
	}
	m := Message{ID: "M1", Agent: "assistant", Direction: DirectionOut, Kind: KindText, Text: "x", TS: base}
	lift := failMessageInserts(t, st)

	if ok, err := st.FireSchedule("A", m, base); err == nil || ok {
		t.Fatalf("fire with a failing insert = %v, %v; want an error", ok, err)
	}
	if got, _ := st.PendingSchedules("assistant"); len(got) != 1 {
		t.Fatalf("the reminder left pending by a failed fire = %+v", got)
	}
	lift()
	if ok, err := st.FireSchedule("A", m, base); err != nil || !ok {
		t.Fatalf("retry = %v, %v", ok, err)
	}
	if ok, _ := st.FireSchedule("A", Message{ID: "M2", Agent: "assistant", Direction: DirectionOut, Kind: KindText, TS: base}, base); ok {
		t.Fatal("a third fire posted")
	}
	if msgs, _, _ := st.Messages("assistant", Page{}); len(msgs) != 1 || msgs[0].ID != "M1" {
		t.Fatalf("messages = %q, want exactly M1", texts(msgs))
	}
}

func TestStoreCancelScheduleErrors(t *testing.T) {
	st := openTestStore(t)
	base := newTestClock().Now()
	for _, id := range []string{"A", "B"} {
		if err := st.InsertSchedule(sched(id, "assistant", base)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CancelSchedule("nope", "assistant", base); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown = %v, want ErrNotFound", err)
	}
	if err := st.CancelSchedule("A", "other", base); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other agent's = %v, want ErrNotFound", err)
	}
	if err := st.CancelSchedule("A", "assistant", base); err != nil {
		t.Fatal(err)
	}
	if err := st.CancelSchedule("A", "assistant", base); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled twice = %v, want ErrConflict", err)
	}
	if _, err := st.FireSchedule("B", Message{ID: "M", Agent: "assistant", Direction: DirectionOut, Kind: KindText, TS: base}, base); err != nil {
		t.Fatal(err)
	}
	if err := st.CancelSchedule("B", "assistant", base); !errors.Is(err, ErrConflict) {
		t.Fatalf("fired = %v, want ErrConflict", err)
	}
}

func TestStoreRoundTripsAskDeadlineReasonAndAsker(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	m := msg(ids, clock, "assistant", DirectionOut, KindAsk, "Ship?")
	m.State = StateOpen
	m.Deadline = clock.Now().Add(10 * time.Minute)
	m.Asker = "task:build"
	m.ClosedReason = ClosedWithdrawn
	mustInsert(t, st, m)
	got, err := st.Message(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deadline.Equal(m.Deadline) || got.Asker != "task:build" || got.ClosedReason != ClosedWithdrawn {
		t.Fatalf("round trip = deadline %v asker %q reason %q", got.Deadline, got.Asker, got.ClosedReason)
	}
	plain := msg(ids, clock, "assistant", DirectionOut, KindText, "hi")
	mustInsert(t, st, plain)
	got, _ = st.Message(plain.ID)
	if !got.Deadline.IsZero() {
		t.Fatalf("no deadline must scan to the zero time, got %v", got.Deadline)
	}
}

func TestCloseAskClosesOnlyAnOpenAsk(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	open := msg(ids, clock, "assistant", DirectionOut, KindAsk, "q")
	open.State = StateOpen
	answered := msg(ids, clock, "assistant", DirectionOut, KindAsk, "q")
	answered.State = StateAnswered
	perm := msg(ids, clock, "assistant", DirectionOut, KindPermission, "Bash")
	perm.State = StateOpen
	for _, m := range []Message{open, answered, perm} {
		mustInsert(t, st, m)
	}
	for _, c := range []struct {
		id   string
		want bool
	}{{open.ID, true}, {open.ID, false}, {answered.ID, false}, {perm.ID, false}} {
		ok, err := st.CloseAsk(c.id, ClosedWithdrawn)
		if err != nil || ok != c.want {
			t.Fatalf("CloseAsk(%s) = %v, %v; want %v", c.id, ok, err, c.want)
		}
	}
	got, _ := st.Message(open.ID)
	if got.State != StateExpired || got.ClosedReason != ClosedWithdrawn {
		t.Fatalf("closed ask = %s/%q", got.State, got.ClosedReason)
	}
	if p, _ := st.Message(perm.ID); p.State != StateOpen {
		t.Fatalf("permission prompt = %s", p.State)
	}
}

func TestCloseAsksOfClosesOnlyThatAskersOpenAsks(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	mine := msg(ids, clock, "assistant", DirectionOut, KindAsk, "q")
	mine.State, mine.Asker = StateOpen, "assistant"
	other := msg(ids, clock, "web", DirectionOut, KindAsk, "q")
	other.State, other.Asker = StateOpen, "web"
	done := msg(ids, clock, "assistant", DirectionOut, KindAsk, "q")
	done.State, done.Asker = StateAnswered, "assistant"
	for _, m := range []Message{mine, other, done} {
		mustInsert(t, st, m)
	}
	closed, err := st.CloseAsksOf("assistant", ClosedAgentGone)
	if err != nil || len(closed) != 1 || closed[0] != mine.ID {
		t.Fatalf("CloseAsksOf = %v, %v; want [%s]", closed, err, mine.ID)
	}
	got, _ := st.Message(mine.ID)
	if got.State != StateExpired || got.ClosedReason != ClosedAgentGone {
		t.Fatalf("closed ask = %s/%q", got.State, got.ClosedReason)
	}
	if o, _ := st.Message(other.ID); o.State != StateOpen {
		t.Fatalf("another asker's ask = %s", o.State)
	}
	if d, _ := st.Message(done.ID); d.State != StateAnswered {
		t.Fatalf("answered ask = %s", d.State)
	}
}

func TestStoreRoundTripsQuestionsInputAndAnswers(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	ids := NewULIDSource(clock.Now, nil)
	m := msg(ids, clock, "assistant", DirectionOut, KindAsk, "Size?")
	m.State = StateOpen
	m.Input = &Input{Type: InputQuestions, Questions: []Question{{ID: "q1", Header: "H", Question: "Size?", Multi: true, AllowText: true, Options: []Option{{Value: "L", Label: "L", Description: "large"}}}}}
	mustInsert(t, st, m)
	ans := &Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"L"}, Text: "x"}}}
	if ok, err := st.Transition(m.ID, []string{StateOpen}, StateAnswered, ans); err != nil || !ok {
		t.Fatalf("Transition = %v, %v", ok, err)
	}
	got, err := st.Message(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(m.Input)
	have, _ := json.Marshal(got.Input)
	if string(want) != string(have) {
		t.Errorf("input = %s, want %s", have, want)
	}
	if qa := got.Answer.Answers["q1"]; len(qa.Values) != 1 || qa.Values[0] != "L" || qa.Text != "x" {
		t.Errorf("answers = %+v", got.Answer)
	}
}
