package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// questionsCard is an AskUserQuestion tool call as a chat card: the input
// clients render, the message text, and what is needed to answer Claude.
type questionsCard struct {
	Input Input
	Text  string
	// raw is the tool input's questions array, kept verbatim to hand back.
	raw json.RawMessage
	// keys is each question's exact text by position: the key of its answer.
	keys []string
}

// askToolInput is the part of AskUserQuestion's tool input chat reads.
type askToolInput struct {
	Questions []struct {
		Question    string `json:"question"`
		Header      string `json:"header"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

// parseQuestions turns AskUserQuestion's tool input into a questions card.
// It errors on anything the card cannot carry, which the caller answers
// with the generic permission card instead. Claude always offers an
// "Other" free-text answer, so every question allows text.
func parseQuestions(toolInput json.RawMessage) (questionsCard, error) {
	var in askToolInput
	if err := json.Unmarshal(toolInput, &in); err != nil {
		return questionsCard{}, fmt.Errorf("reading AskUserQuestion input: %w", err)
	}
	var verbatim struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(toolInput, &verbatim); err != nil {
		return questionsCard{}, fmt.Errorf("reading AskUserQuestion input: %w", err)
	}
	questions := make([]Question, len(in.Questions))
	keys := make([]string, len(in.Questions))
	for i, qn := range in.Questions {
		options := make([]Option, len(qn.Options))
		for j, o := range qn.Options {
			options[j] = Option{Value: o.Label, Label: o.Label, Description: o.Description}
		}
		questions[i] = Question{ID: fmt.Sprintf("q%d", i+1), Header: qn.Header, Question: qn.Question, Multi: qn.MultiSelect, AllowText: true, Options: options}
		keys[i] = qn.Question
	}
	input, err := validateInput(Input{Type: InputQuestions, Questions: questions})
	if err != nil {
		return questionsCard{}, err
	}
	text := strings.TrimSpace(input.Questions[0].Question)
	if n := len(input.Questions) - 1; n > 0 {
		text = clipRunes(text, maxPermissionSummaryRunes) + fmt.Sprintf(" (+%d more)", n)
	}
	return questionsCard{Input: input, Text: clipRunes(text, maxPermissionSummaryRunes+16), raw: verbatim.Questions, keys: keys}, nil
}

// updatedInput builds the tool input Claude receives in place of its own:
// the original questions array verbatim plus answers keyed by exact
// question text. A single-select answer is its label or the free text; a
// multi-select answer is the chosen labels with any free text last.
func (c questionsCard) updatedInput(a Answer) json.RawMessage {
	answers := make(map[string]any, len(c.keys))
	for i, qn := range c.Input.Questions {
		qa := a.Answers[qn.ID]
		text := strings.TrimSpace(qa.Text)
		if qn.Multi {
			chosen := append([]string{}, qa.Values...)
			if text != "" {
				chosen = append(chosen, text)
			}
			answers[c.keys[i]] = chosen
			continue
		}
		if len(qa.Values) > 0 {
			answers[c.keys[i]] = qa.Values[0]
		} else {
			answers[c.keys[i]] = text
		}
	}
	out, _ := json.Marshal(struct {
		Questions json.RawMessage `json:"questions"`
		Answers   map[string]any  `json:"answers"`
	}{c.raw, answers})
	return out
}
