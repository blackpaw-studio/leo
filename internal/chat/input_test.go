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
