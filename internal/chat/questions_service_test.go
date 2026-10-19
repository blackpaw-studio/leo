package chat

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// askPayload is a PermissionRequest hook payload for AskUserQuestion.
func askPayload(toolInput string) json.RawMessage {
	return json.RawMessage(`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":` + toolInput + `}`)
}

// requestQuestions runs RequestPermission for toolInput on a goroutine and
// returns its decision channel once the card is stored.
func requestQuestions(t *testing.T, f *harnessFixture, ctx context.Context, toolInput string) (<-chan Decision, Message) {
	t.Helper()
	done := make(chan Decision, 1)
	go func() {
		done <- f.svc.RequestPermission(ctx, "assistant", askPayload(toolInput), 2*time.Minute)
	}()
	<-f.timers.got
	return done, f.thread(t, "assistant")[0]
}

func TestAskUserQuestionPostsOneQuestionsCard(t *testing.T) {
	f := newFixture(t)
	_, m := requestQuestions(t, f, context.Background(), sampleAskInput)
	if m.Kind != KindAsk || m.Permission != nil || m.Asker != "" || m.State != StateOpen {
		t.Fatalf("card = %+v", m)
	}
	if m.Text != "Which size? (+1 more)" {
		t.Errorf("text = %q", m.Text)
	}
	if m.Input == nil || m.Input.Type != InputQuestions || len(m.Input.Questions) != 2 || m.Input.Questions[1].ID != "q2" || !m.Input.Questions[1].Multi {
		t.Fatalf("input = %+v", m.Input)
	}
}

func TestAnsweringTheQuestionsCardReturnsUpdatedInput(t *testing.T) {
	f := newFixture(t)
	done, m := requestQuestions(t, f, context.Background(), sampleAskInput)
	answer := Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"L"}}, "q2": {Values: []string{"Ham"}, Text: " pickles "}}}
	if _, err := f.svc.Answer("assistant", m.ID, answer); err != nil {
		t.Fatal(err)
	}
	got := <-done
	want := `{"questions":` + compactQuestions(t, sampleAskInput) + `,"answers":{"Toppings?":["Ham","pickles"],"Which size? ":"L"}}`
	if got.Behavior != "allow" || string(got.UpdatedInput) != want {
		t.Fatalf("decision = %s / %s\nwant %s", got.Behavior, got.UpdatedInput, want)
	}
	if stored := f.thread(t, "assistant")[0]; stored.State != StateAnswered {
		t.Fatalf("stored = %s %+v", stored.State, stored.Answer)
	}
}

func TestMalformedAskUserQuestionFallsBackToPermissionCard(t *testing.T) {
	f := newFixture(t)
	done := make(chan Decision, 1)
	go func() {
		done <- f.svc.RequestPermission(context.Background(), "assistant", askPayload(`{"questions":[]}`), time.Minute)
	}()
	<-f.timers.got
	m := f.thread(t, "assistant")[0]
	if m.Kind != KindPermission || m.Permission == nil || m.Permission.Tool != "AskUserQuestion" {
		t.Fatalf("fallback card = %+v", m)
	}
	f.timers.fireLast()
	<-done
}

func TestQuestionsCardTimeoutExpiresWithReasonAndNoDecision(t *testing.T) {
	f := newFixture(t)
	done, _ := requestQuestions(t, f, context.Background(), sampleAskInput)
	f.timers.fireLast()
	if got := <-done; !got.IsZero() {
		t.Fatalf("timeout decision = %+v, want none", got)
	}
	if m := f.thread(t, "assistant")[0]; m.State != StateExpired || m.ClosedReason != ClosedTimeout {
		t.Fatalf("card = %s/%q", m.State, m.ClosedReason)
	}
}

func TestQuestionsCardHookGoneMarksAnsweredInTerminal(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done, _ := requestQuestions(t, f, ctx, sampleAskInput)
	cancel()
	if got := <-done; !got.IsZero() {
		t.Fatalf("decision = %+v, want none", got)
	}
	m := f.thread(t, "assistant")[0]
	if m.State != StateAnswered || m.Answer == nil || !m.Answer.isTerminal() || len(m.Answer.Answers) != 0 {
		t.Fatalf("card = %s/%+v", m.State, m.Answer)
	}
}

func TestShutdownClosesAnOpenQuestionsCardAsTimeout(t *testing.T) {
	f := newFixture(t)
	done, _ := requestQuestions(t, f, context.Background(), sampleAskInput)
	f.svc.Close()
	if got := <-done; !got.IsZero() {
		t.Fatalf("decision = %+v, want none", got)
	}
	if m := f.thread(t, "assistant")[0]; m.State != StateExpired || m.ClosedReason != ClosedTimeout {
		t.Fatalf("card = %s/%q", m.State, m.ClosedReason)
	}
}

func TestStartupClosesQuestionsCardsLeftOpen(t *testing.T) {
	f := newFixture(t)
	card, err := parseQuestions(json.RawMessage(sampleAskInput))
	if err != nil {
		t.Fatal(err)
	}
	m := Message{ID: NewULIDSource(f.clock.Now, nil).New(), Agent: "assistant", Direction: DirectionOut, Kind: KindAsk, Text: card.Text, State: StateOpen, Input: &card.Input, TS: f.clock.Now()}
	if err := f.store.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.Message(m.ID); got.State != StateExpired || got.ClosedReason != ClosedTimeout {
		t.Fatalf("card = %s/%q: its hook died with the daemon", got.State, got.ClosedReason)
	}
}

func TestAnswerTrimsEachQuestionText(t *testing.T) {
	f := newFixture(t)
	done, m := requestQuestions(t, f, context.Background(), sampleAskInput)
	answer := Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"L"}}, "q2": {Text: "  pickles\n"}}}
	if _, err := f.svc.Answer("assistant", m.ID, answer); err != nil {
		t.Fatal(err)
	}
	<-done
	if stored := f.thread(t, "assistant")[0]; stored.Answer.Answers["q2"].Text != "pickles" {
		t.Fatalf("stored answers = %+v", stored.Answer.Answers)
	}
	blank := Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"L"}}, "q2": {Text: " \n "}}}
	if _, err := f.svc.Answer("assistant", m.ID, blank); err == nil {
		t.Fatal("blank text must not count as an answer")
	}
}

// openCard stores an open questions card whose asker is its agent, the shape
// that late delivery would otherwise reply to.
func openCard(t *testing.T, f *harnessFixture) Message {
	t.Helper()
	card, err := parseQuestions(json.RawMessage(sampleAskInput))
	if err != nil {
		t.Fatal(err)
	}
	m := Message{ID: NewULIDSource(f.clock.Now, nil).New(), Agent: "assistant", Direction: DirectionOut, Kind: KindAsk, Text: card.Text, State: StateOpen, Input: &card.Input, Asker: "assistant", TS: f.clock.Now()}
	if err := f.store.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestQuestionsCardAnswerIsNeverDeliveredAsAReply(t *testing.T) {
	f := newFixture(t)
	m := openCard(t, f)
	answer := Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"L"}}, "q2": {Values: []string{"Ham"}}}}
	if _, err := f.svc.Answer("assistant", m.ID, answer); err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 0 {
		t.Fatalf("a questions answer was delivered as a chat message: %q", got)
	}
	if thread := f.thread(t, "assistant"); len(thread) != 1 {
		t.Fatalf("thread holds %d messages, want only the card", len(thread))
	}
}

func TestAnsweringAnExpiredQuestionsCardConflicts(t *testing.T) {
	f := newFixture(t)
	done, m := requestQuestions(t, f, context.Background(), sampleAskInput)
	f.timers.fireLast()
	<-done
	answer := Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"L"}}, "q2": {Values: []string{"Ham"}}}}
	if _, err := f.svc.Answer("assistant", m.ID, answer); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 0 {
		t.Fatalf("delivered %q", got)
	}
}

func TestResumeAskRefusesQuestionsCards(t *testing.T) {
	f := newFixture(t)
	m := openCard(t, f)
	_, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: m.ID})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}
