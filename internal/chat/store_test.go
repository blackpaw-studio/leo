package chat

import (
	"path/filepath"
	"testing"
	"time"
)

// testClock is a settable clock for the store and service.
type testClock struct{ t time.Time }

func (c *testClock) Now() time.Time          { return c.t }
func (c *testClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

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

	ok, err := st.Transition(m.ID, []string{StateOpen}, StateAnswered, "yes")
	if err != nil || !ok {
		t.Fatalf("first transition = %v, %v", ok, err)
	}
	ok, err = st.Transition(m.ID, []string{StateOpen}, StateExpired, "")
	if err != nil || ok {
		t.Fatalf("second transition must lose: %v, %v", ok, err)
	}
	got, err := st.Message(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateAnswered || got.Answer != "yes" {
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
	mustInsert(t, st, m)
	if err := st.LinkAttachments(m.ID, "assistant", []string{att.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkAttachments(m.ID, "assistant", []string{att.ID}); err == nil {
		t.Fatal("an attachment links to one message only")
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
	m := mustInsert(t, st, msg(ids, clock, "assistant", DirectionIn, KindFile, ""))
	if err := st.LinkAttachments(m.ID, "assistant", []string{att.ID}); err != nil {
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
	m := mustInsert(t, st, msg(ids, clock, "assistant", DirectionIn, KindFile, ""))
	if err := st.LinkAttachments(m.ID, "assistant", []string{att.ID}); err == nil {
		t.Fatal("linking another agent's upload must fail")
	}
}

func TestStoreCapturesRoundTrip(t *testing.T) {
	st := openTestStore(t)
	clock := newTestClock()
	c := Capture{MessageID: "01M4RMAZ800000000000000001", Agent: "assistant", Prompt: "[Leo Chat] hi", DeliveredAt: clock.Now()}
	if err := st.AddCapture(c); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkCapturesToolPosted("assistant"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Captures("assistant")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].ToolPosted || got[0].Prompt != c.Prompt || !got[0].DeliveredAt.Equal(c.DeliveredAt) {
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
