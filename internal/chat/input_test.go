package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var abc = []Option{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}, {Value: "c", Label: "C"}}

func TestResolveInput(t *testing.T) {
	ok := map[string]struct {
		options []Option
		in      *Input
		want    Input
	}{
		"legacy options become buttons": {options: []Option{{Value: "y"}}, want: Input{Type: InputButtons, Options: []Option{{Value: "y", Label: "y"}}}},
		"checkboxes default to 0..len":  {in: &Input{Type: InputCheckboxes, Options: abc}, want: Input{Type: InputCheckboxes, Options: abc, Min: 0, Max: ip(3)}},
		"radio with free text":          {in: &Input{Type: InputRadio, Options: abc, AllowText: true, Placeholder: "other"}, want: Input{Type: InputRadio, Options: abc, AllowText: true, Placeholder: "other"}},
		"text":                          {in: &Input{Type: InputText, Placeholder: "why?"}, want: Input{Type: InputText, Placeholder: "why?"}},
	}
	for name, c := range ok {
		got, err := resolveInput(c.options, c.in)
		if err != nil || !sameInput(got, c.want) {
			t.Errorf("%s: got %+v, %v; want %+v", name, got, err, c.want)
		}
	}
	many := make([]Option, MaxAskOptions+1)
	for i := range many {
		many[i] = Option{Value: string(rune('a' + i))}
	}
	bad := map[string]struct {
		options []Option
		in      *Input
	}{
		"nothing given":               {},
		"options and input":           {options: abc, in: &Input{Type: InputButtons, Options: abc}},
		"unknown type":                {in: &Input{Type: "slider", Options: abc}},
		"buttons without options":     {in: &Input{Type: InputButtons}},
		"too many options":            {in: &Input{Type: InputRadio, Options: many}},
		"duplicate values":            {in: &Input{Type: InputRadio, Options: []Option{{Value: "a"}, {Value: "a"}}}},
		"text with options":           {in: &Input{Type: InputText, Options: abc}},
		"text with allow_text":        {in: &Input{Type: InputText, AllowText: true}},
		"buttons with min":            {in: &Input{Type: InputButtons, Options: abc, Min: 1}},
		"radio with max":              {in: &Input{Type: InputRadio, Options: abc, Max: ip(2)}},
		"min above max":               {in: &Input{Type: InputCheckboxes, Options: abc, Min: 3, Max: ip(2)}},
		"max above option count":      {in: &Input{Type: InputCheckboxes, Options: abc, Max: ip(4)}},
		"explicit max 0":              {in: &Input{Type: InputCheckboxes, Options: abc, Max: ip(0)}},
		"explicit max below min":      {in: &Input{Type: InputCheckboxes, Options: abc, Min: 2, Max: ip(1)}},
		"text with explicit max 0":    {in: &Input{Type: InputText, Max: ip(0)}},
		"radio with explicit max 0":   {in: &Input{Type: InputRadio, Options: abc, Max: ip(0)}},
		"negative min":                {in: &Input{Type: InputCheckboxes, Options: abc, Min: -1}},
		"placeholder on plain choice": {in: &Input{Type: InputRadio, Options: abc, Placeholder: "x"}},
		"placeholder too long":        {in: &Input{Type: InputText, Placeholder: strings.Repeat("x", MaxPlaceholderRunes+1)}},
		"empty option value":          {in: &Input{Type: InputButtons, Options: []Option{{Value: " "}}}},
	}
	for name, c := range bad {
		if _, err := resolveInput(c.options, c.in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func ip(n int) *int { return &n }

func sameInput(a, b Input) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(normalizeLabels(b))
	return string(x) == string(y)
}

func normalizeLabels(in Input) Input {
	opts := make([]Option, len(in.Options))
	for i, o := range in.Options {
		if o.Label == "" {
			o.Label = o.Value
		}
		opts[i] = o
	}
	if len(opts) == 0 {
		opts = nil
	}
	in.Options = opts
	return in
}

func TestValidAnswerByInputType(t *testing.T) {
	long := strings.Repeat("x", MaxAnswerTextRunes+1)
	cb := Input{Type: InputCheckboxes, Options: abc, Min: 1, Max: ip(2)}
	cbText := Input{Type: InputCheckboxes, Options: abc, Min: 1, Max: ip(2), AllowText: true}
	btn := Input{Type: InputButtons, Options: abc}
	radioText := Input{Type: InputRadio, Options: abc, AllowText: true}
	txt := Input{Type: InputText}
	cases := []struct {
		name string
		in   Input
		ans  Answer
		ok   bool
	}{
		{"button one value", btn, Answer{Values: []string{"a"}}, true},
		{"button unknown value", btn, Answer{Values: []string{"z"}}, false},
		{"button two values", btn, Answer{Values: []string{"a", "b"}}, false},
		{"button none", btn, Answer{}, false},
		{"button text without allow_text", btn, Answer{Values: []string{"a"}, Text: "hi"}, false},
		{"radio text only", radioText, Answer{Text: "other"}, true},
		{"radio value and text", radioText, Answer{Values: []string{"a"}, Text: "x"}, true},
		{"radio nothing", radioText, Answer{}, false},
		{"checkboxes in range", cb, Answer{Values: []string{"a", "c"}}, true},
		{"checkboxes below min", cb, Answer{}, false},
		{"checkboxes above max", cb, Answer{Values: []string{"a", "b", "c"}}, false},
		{"checkboxes duplicate", cb, Answer{Values: []string{"a", "a"}}, false},
		{"checkboxes unknown", cb, Answer{Values: []string{"a", "z"}}, false},
		{"checkboxes text without allow_text", cb, Answer{Values: []string{"a"}, Text: "x"}, false},
		{"checkboxes text replaces empty selection despite min", cbText, Answer{Text: "none fit"}, true},
		{"checkboxes text accompanies selection", cbText, Answer{Values: []string{"b"}, Text: "and more"}, true},
		{"checkboxes allow_text but empty everything", cbText, Answer{}, false},
		{"text ok", txt, Answer{Text: "hello"}, true},
		{"text with values", txt, Answer{Values: []string{"a"}, Text: "hello"}, false},
		{"text empty", txt, Answer{}, false},
		{"text too long", txt, Answer{Text: long}, false},
		{"choice text too long", radioText, Answer{Text: long}, false},
	}
	for _, c := range cases {
		err := validAnswer(Message{Kind: KindAsk, Input: &c.in}, c.ans)
		if c.ok && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
	}
	if err := validAnswer(Message{Kind: KindText}, Answer{Values: []string{"a"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("answering a text message: %v", err)
	}
}

// askAndAnswer runs one ask to its answer and returns the tool result.
func askAndAnswer(t *testing.T, f *harnessFixture, req AskRequest, ans Answer) (AskResult, Message) {
	t.Helper()
	req.Caller, req.Question = "assistant", "Which?"
	// A deadline-free ask arms no timer of its own; the call window is the
	// timer the test waits on, and it keeps the call blocked on the answer.
	if req.Wait == 0 {
		req.Wait = 30 * time.Minute
	}
	done := make(chan AskResult, 1)
	go func() {
		r, err := f.svc.Ask(context.Background(), req)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	<-f.timers.got
	open := f.thread(t, "assistant")[0]
	if _, err := f.svc.Answer("assistant", open.ID, ans); err != nil {
		t.Fatal(err)
	}
	return <-done, open
}

func TestAskCheckboxesRoundTrip(t *testing.T) {
	f := newFixture(t)
	res, open := askAndAnswer(t, f, AskRequest{Input: &Input{Type: InputCheckboxes, Options: abc}}, Answer{Values: []string{"a", "c"}})
	if got, _ := json.Marshal(res); string(got) != `{"values":["a","c"],"labels":["A","C"],"text":"","timed_out":false,"message_id":"`+open.ID+`"}` {
		t.Fatalf("result = %s", got)
	}
	if open.Input == nil || open.Input.Type != InputCheckboxes || open.Input.Max == nil || *open.Input.Max != 3 {
		t.Fatalf("stored input = %+v", open.Input)
	}
	m, _ := f.store.Message(open.ID)
	if m.Answer == nil || strings.Join(m.Answer.Values, ",") != "a,c" {
		t.Fatalf("stored answer = %+v", m.Answer)
	}
}

func TestAskTextAndFreeTextRoundTrip(t *testing.T) {
	f := newFixture(t)
	res, _ := askAndAnswer(t, f, AskRequest{Input: &Input{Type: InputText}}, Answer{Text: "  ship it  "})
	if res.Text != "ship it" || len(res.Values) != 0 || res.Labels == nil || res.TimedOut {
		t.Fatalf("text result = %+v", res)
	}
	res, _ = askAndAnswer(t, f, AskRequest{Input: &Input{Type: InputRadio, Options: abc, AllowText: true}}, Answer{Text: "d"})
	if res.Text != "d" || len(res.Values) != 0 {
		t.Fatalf("free-text radio result = %+v", res)
	}
}

func TestAskLegacyOptionsStillButtons(t *testing.T) {
	f := newFixture(t)
	res, open := askAndAnswer(t, f, AskRequest{Options: []Option{{Value: "y", Label: "Yes"}}}, Answer{Values: []string{"y"}})
	if open.Input == nil || open.Input.Type != InputButtons || len(res.Values) != 1 || res.Labels[0] != "Yes" {
		t.Fatalf("result = %+v, input = %+v", res, open.Input)
	}
}

func TestAskRejectsBadInputAndBothShapes(t *testing.T) {
	f := newFixture(t)
	for name, req := range map[string]AskRequest{
		"options and input": {Options: abc, Input: &Input{Type: InputRadio, Options: abc}},
		"unknown type":      {Input: &Input{Type: "dial", Options: abc}},
		"neither":           {},
	} {
		req.Caller, req.Question = "assistant", "?"
		if _, err := f.svc.Ask(context.Background(), req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestAnswerRefusesInvalidAnswerAndKeepsAskOpen(t *testing.T) {
	f := newFixture(t)
	go func() {
		_, _ = f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Input: &Input{Type: InputCheckboxes, Options: abc, Min: 2}, Timeout: time.Hour})
	}()
	<-f.timers.got
	open := f.thread(t, "assistant")[0]
	if _, err := f.svc.Answer("assistant", open.ID, Answer{Values: []string{"a"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("below min: %v", err)
	}
	if m, _ := f.store.Message(open.ID); m.State != StateOpen {
		t.Fatalf("state = %s", m.State)
	}
}

func TestResumeReturnsTypedAnswer(t *testing.T) {
	f := newFixture(t)
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Input: &Input{Type: InputCheckboxes, Options: abc}, Timeout: time.Hour, Wait: time.Minute})
		done <- r
	}()
	<-f.timers.got
	<-f.timers.got
	f.timers.fireLast()
	first := <-done
	if !first.Pending {
		t.Fatalf("first = %+v", first)
	}
	if _, err := f.svc.Answer("assistant", first.MessageID, Answer{Values: []string{"b"}, Text: ""}); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: first.MessageID})
	if err != nil || len(r.Values) != 1 || r.Values[0] != "b" || r.Labels[0] != "B" || r.Pending {
		t.Fatalf("resumed = %+v, %v", r, err)
	}
	// Resume refuses a new-ask field.
	if _, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: first.MessageID, Input: &Input{Type: InputText}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resume with input: %v", err)
	}
}

// A valid empty selection is an answer, not the absence of one.
func TestEmptyCheckboxSelectionIsAnAnswer(t *testing.T) {
	f := newFixture(t)
	res, open := askAndAnswer(t, f, AskRequest{Input: &Input{Type: InputCheckboxes, Options: abc}}, Answer{})
	if res.TimedOut || res.Pending || len(res.Values) != 0 {
		t.Fatalf("result = %+v, want an answered, empty selection", res)
	}
	m, _ := f.store.Message(open.ID)
	if m.State != StateAnswered || m.Answer == nil || len(m.Answer.Values) != 0 {
		t.Fatalf("stored = %s, %+v", m.State, m.Answer)
	}
	if b, _ := json.Marshal(m.Answer); string(b) != `{"values":[],"text":""}` {
		t.Fatalf("answer JSON = %s", b)
	}
	resumed, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: open.ID})
	if err != nil || resumed.TimedOut || resumed.Pending {
		t.Fatalf("resumed = %+v, %v", resumed, err)
	}
}

func TestCheckboxesWithAllowTextNeedTextForAnEmptySelection(t *testing.T) {
	in := Input{Type: InputCheckboxes, Options: abc, Max: ip(3), AllowText: true}
	if err := validAnswerFor(in, Answer{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty selection and empty text: %v", err)
	}
	if err := validAnswerFor(in, Answer{Text: "none fit"}); err != nil {
		t.Fatal(err)
	}
	plain := Input{Type: InputCheckboxes, Options: abc, Max: ip(3)}
	if err := validAnswerFor(plain, Answer{}); err != nil {
		t.Fatalf("plain checkboxes, min 0: %v", err)
	}
}

func qs(questions ...Question) Input {
	return Input{Type: InputQuestions, Questions: questions}
}

func q(id, text string, multi bool, labels ...string) Question {
	opts := make([]Option, len(labels))
	for i, l := range labels {
		opts[i] = Option{Value: l, Label: l}
	}
	return Question{ID: id, Header: "H", Question: text, Multi: multi, AllowText: true, Options: opts}
}

func TestValidateQuestionsInput(t *testing.T) {
	good := qs(q("q1", "Size?", false, "S", "L"), q("q2", "Toppings?", true, "Ham", "Egg"))
	got, err := validateInput(good)
	if err != nil || len(got.Questions) != 2 {
		t.Fatalf("valid questions: %+v, %v", got, err)
	}
	five := make([]Question, MaxAskQuestions+1)
	for i := range five {
		five[i] = q("q"+string(rune('1'+i)), "Q"+string(rune('1'+i)), false, "a")
	}
	bad := map[string]Input{
		"no questions":             {Type: InputQuestions},
		"too many questions":       qs(five...),
		"top-level options":        {Type: InputQuestions, Options: abc, Questions: good.Questions},
		"top-level allow_text":     {Type: InputQuestions, AllowText: true, Questions: good.Questions},
		"top-level max":            {Type: InputQuestions, Max: ip(1), Questions: good.Questions},
		"top-level min":            {Type: InputQuestions, Min: 1, Questions: good.Questions},
		"top-level placeholder":    {Type: InputQuestions, Placeholder: "x", Questions: good.Questions},
		"ids out of position":      qs(q("q2", "Size?", false, "S")),
		"blank question":           qs(q("q1", "  ", false, "S")),
		"duplicate questions":      qs(q("q1", "Size?", false, "S"), q("q2", "Size?", false, "L")),
		"question w/o options":     qs(q("q1", "Size?", false)),
		"duplicate labels":         qs(q("q1", "Size?", false, "S", "S")),
		"value differs from label": qs(Question{ID: "q1", Question: "Size?", Options: []Option{{Value: "s", Label: "S"}}}),
	}
	for name, in := range bad {
		if _, err := validateInput(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := resolveInput(nil, &good); !errors.Is(err, ErrInvalid) {
		t.Errorf("leo_chat_ask must not build a questions input: %v", err)
	}
}

func TestValidAnswerForQuestions(t *testing.T) {
	in := qs(q("q1", "Size?", false, "S", "L"), q("q2", "Toppings?", true, "Ham", "Egg"))
	noText := qs(Question{ID: "q1", Question: "Size?", Options: []Option{{Value: "S", Label: "S"}}})
	long := strings.Repeat("x", MaxAnswerTextRunes+1)
	qa := func(v string, text string) QuestionAnswer {
		a := QuestionAnswer{Text: text}
		if v != "" {
			a.Values = []string{v}
		}
		return a
	}
	ok := map[string]map[string]QuestionAnswer{
		"values for each":    {"q1": qa("S", ""), "q2": {Values: []string{"Ham", "Egg"}}},
		"text for single":    {"q1": qa("", "XL"), "q2": qa("Ham", "")},
		"multi value + text": {"q1": qa("S", ""), "q2": {Values: []string{"Ham"}, Text: "pickles"}},
		"multi text only":    {"q1": qa("S", ""), "q2": qa("", "none")},
	}
	for name, a := range ok {
		if err := validAnswerFor(in, Answer{Answers: a}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]Answer{
		"no answers":            {},
		"missing id":            {Answers: map[string]QuestionAnswer{"q1": qa("S", "")}},
		"unknown id":            {Answers: map[string]QuestionAnswer{"q1": qa("S", ""), "q2": qa("Ham", ""), "q3": qa("S", "")}},
		"empty question":        {Answers: map[string]QuestionAnswer{"q1": qa("S", ""), "q2": {}}},
		"blank text only":       {Answers: map[string]QuestionAnswer{"q1": qa("S", ""), "q2": qa("", "  ")}},
		"single two values":     {Answers: map[string]QuestionAnswer{"q1": {Values: []string{"S", "L"}}, "q2": qa("Ham", "")}},
		"single value and text": {Answers: map[string]QuestionAnswer{"q1": qa("S", "XL"), "q2": qa("Ham", "")}},
		"multi duplicate":       {Answers: map[string]QuestionAnswer{"q1": qa("S", ""), "q2": {Values: []string{"Ham", "Ham"}}}},
		"unknown option":        {Answers: map[string]QuestionAnswer{"q1": qa("Z", ""), "q2": qa("Ham", "")}},
		"text too long":         {Answers: map[string]QuestionAnswer{"q1": qa("S", ""), "q2": qa("", long)}},
		"top-level values":      {Values: []string{"S"}, Answers: map[string]QuestionAnswer{"q1": qa("S", ""), "q2": qa("Ham", "")}},
		"top-level text":        {Text: "hi", Answers: map[string]QuestionAnswer{"q1": qa("S", ""), "q2": qa("Ham", "")}},
	}
	for name, a := range bad {
		if err := validAnswerFor(in, a); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if err := validAnswerFor(noText, Answer{Answers: map[string]QuestionAnswer{"q1": qa("", "x")}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("text on a question without allow_text: %v", err)
	}
	// answers belong to the questions type only
	if err := validAnswerFor(Input{Type: InputButtons, Options: abc}, Answer{Values: []string{"a"}, Answers: map[string]QuestionAnswer{}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("answers on a buttons ask: %v", err)
	}
	if err := validAnswerFor(Input{Type: InputText}, Answer{Text: "x", Answers: map[string]QuestionAnswer{"q1": {}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("answers on a text ask: %v", err)
	}
}

func TestQuestionsWireShape(t *testing.T) {
	in := qs(Question{ID: "q1", Header: "Size", Question: "Size?", AllowText: true, Options: []Option{{Value: "L", Label: "L", Description: "large"}}})
	got, _ := json.Marshal(in)
	want := `{"type":"questions","questions":[{"id":"q1","header":"Size","question":"Size?","multi":false,"allow_text":true,"options":[{"value":"L","label":"L","description":"large"}]}]}`
	if string(got) != want {
		t.Errorf("input =\n%s\nwant\n%s", got, want)
	}
	ans, _ := json.Marshal(Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"L"}}}})
	if string(ans) != `{"values":[],"text":"","answers":{"q1":{"values":["L"],"text":""}}}` {
		t.Errorf("answer = %s", ans)
	}
	empty, _ := json.Marshal(Answer{Answers: map[string]QuestionAnswer{"q1": {}}})
	if string(empty) != `{"values":[],"text":"","answers":{"q1":{"values":[],"text":""}}}` {
		t.Errorf("empty-values answer = %s", empty)
	}
	btn, _ := json.Marshal(Input{Type: InputButtons, Options: []Option{{Value: "a", Label: "A"}}})
	if string(btn) != `{"type":"buttons","options":[{"value":"a","label":"A"}]}` {
		t.Errorf("description must be omitted when empty: %s", btn)
	}
	if (Answer{Values: []string{AnswerTerminal}, Answers: map[string]QuestionAnswer{"q1": {}}}).isTerminal() {
		t.Error("an answer carrying answers is not the terminal marker")
	}
}
