package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleAskInput = `{"questions":[{"question":"Which size? ","header":"Size","options":[{"label":"S","description":"small"},{"label":"L"}],"multiSelect":false,"x":1},{"question":"Toppings?","header":"Top","options":[{"label":"Ham","description":"pig"},{"label":"Egg","description":"hen"}],"multiSelect":true}]}`

func mustParseQuestions(t *testing.T, raw string) questionsCard {
	t.Helper()
	card, err := parseQuestions(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parseQuestions: %v", err)
	}
	return card
}

func TestParseQuestionsBuildsTheCard(t *testing.T) {
	card := mustParseQuestions(t, sampleAskInput)
	got, _ := json.Marshal(card.Input)
	want := `{"type":"questions","questions":[` +
		`{"id":"q1","header":"Size","question":"Which size? ","multi":false,"allow_text":true,"options":[{"value":"S","label":"S","description":"small"},{"value":"L","label":"L"}]},` +
		`{"id":"q2","header":"Top","question":"Toppings?","multi":true,"allow_text":true,"options":[{"value":"Ham","label":"Ham","description":"pig"},{"value":"Egg","label":"Egg","description":"hen"}]}]}`
	if string(got) != want {
		t.Errorf("input =\n%s\nwant\n%s", got, want)
	}
	if card.Text != "Which size? (+1 more)" {
		t.Errorf("text = %q", card.Text)
	}
	one := mustParseQuestions(t, `{"questions":[{"question":"Only?","header":"h","options":[{"label":"a"}],"multiSelect":false}]}`)
	if one.Text != "Only?" {
		t.Errorf("single-question text = %q", one.Text)
	}
}

func TestParseQuestionsRefusesWhatTheCardCannotCarry(t *testing.T) {
	opt := `{"label":"a"}`
	q := func(n string) string {
		return `{"question":"` + n + `","header":"h","options":[` + opt + `],"multiSelect":false}`
	}
	bad := map[string]string{
		"not json":            `nope`,
		"no questions":        `{"questions":[]}`,
		"missing field":       `{}`,
		"questions not array": `{"questions":{"a":1}}`,
		"five questions":      `{"questions":[` + strings.Join([]string{q("1"), q("2"), q("3"), q("4"), q("5")}, ",") + `]}`,
		"duplicate questions": `{"questions":[` + q("same") + `,` + q("same") + `]}`,
		"duplicate labels":    `{"questions":[{"question":"Q","header":"h","options":[{"label":"a"},{"label":"a"}],"multiSelect":false}]}`,
		"blank question":      `{"questions":[` + q(" ") + `]}`,
		"no options":          `{"questions":[{"question":"Q","header":"h","options":[],"multiSelect":false}]}`,
	}
	for name, raw := range bad {
		if _, err := parseQuestions(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestUpdatedInputMapsAnswersBackToQuestionText(t *testing.T) {
	card := mustParseQuestions(t, sampleAskInput)
	cases := map[string]struct {
		answers map[string]QuestionAnswer
		want    string
	}{
		"labels": {
			map[string]QuestionAnswer{"q1": {Values: []string{"S"}}, "q2": {Values: []string{"Ham", "Egg"}}},
			`{"Toppings?":["Ham","Egg"],"Which size? ":"S"}`,
		},
		"single text only": {
			map[string]QuestionAnswer{"q1": {Text: "XL"}, "q2": {Values: []string{"Egg"}}},
			`{"Toppings?":["Egg"],"Which size? ":"XL"}`,
		},
		"multi text appended last": {
			map[string]QuestionAnswer{"q1": {Values: []string{"L"}}, "q2": {Values: []string{"Ham", "Egg"}, Text: "pickles"}},
			`{"Toppings?":["Ham","Egg","pickles"],"Which size? ":"L"}`,
		},
		"multi text only": {
			map[string]QuestionAnswer{"q1": {Values: []string{"L"}}, "q2": {Text: "none"}},
			`{"Toppings?":["none"],"Which size? ":"L"}`,
		},
	}
	for name, c := range cases {
		got := string(card.updatedInput(Answer{Answers: c.answers}))
		if want := `{"questions":` + compactQuestions(t, sampleAskInput) + `,"answers":` + c.want + `}`; got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
}

// compactQuestions is the questions array of tool input, as it must pass
// through to Claude: every field, including ones chat does not know.
func compactQuestions(t *testing.T, toolInput string) string {
	t.Helper()
	var in struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(toolInput), &in); err != nil {
		t.Fatal(err)
	}
	return string(in.Questions)
}

func TestUpdatedInputKeepsUnknownQuestionFields(t *testing.T) {
	card := mustParseQuestions(t, sampleAskInput)
	out := card.updatedInput(Answer{Answers: map[string]QuestionAnswer{"q1": {Values: []string{"S"}}, "q2": {Values: []string{"Ham"}}}})
	var parsed struct {
		Questions []map[string]any `json:"questions"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Questions[0]["x"] != float64(1) || parsed.Questions[0]["question"] != "Which size? " {
		t.Errorf("questions did not pass through verbatim: %v", parsed.Questions[0])
	}
}
