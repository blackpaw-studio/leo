package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Ask input types: how an ask is answered in the apps.
const (
	InputButtons    = "buttons"    // tapping a choice submits it
	InputRadio      = "radio"      // select one choice, then submit
	InputCheckboxes = "checkboxes" // select within [Min, Max] choices, then submit
	InputText       = "text"       // one free-text field
)

// Bounds on ask inputs and answers.
const (
	MaxAnswerTextRunes  = 4000
	MaxPlaceholderRunes = 200
)

// Input is the answer control of an ask message (and, as buttons, of a
// permission message). Max is a pointer so omitted (nil: all options) is
// distinct from an explicit 0, which is invalid; it is always set once a
// checkboxes input is resolved.
type Input struct {
	Type        string   `json:"type"`
	Options     []Option `json:"options,omitempty"`
	AllowText   bool     `json:"allow_text,omitempty"`
	Min         int      `json:"min,omitempty"`
	Max         *int     `json:"max,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// Answer is a message's answer: the chosen option values and/or free text.
type Answer struct {
	Values []string `json:"values"`
	Text   string   `json:"text"`
}

// MarshalJSON writes values as [] rather than null.
func (a Answer) MarshalJSON() ([]byte, error) {
	type wire Answer // no methods: avoids recursion
	if a.Values == nil {
		a.Values = []string{}
	}
	return json.Marshal(wire(a))
}

// terminalAnswer is the answer recorded when a permission prompt was
// settled in the agent's own terminal.
var terminalAnswer = &Answer{Values: []string{AnswerTerminal}}

// isTerminal reports whether the answer says the terminal settled it.
func (a Answer) isTerminal() bool {
	return len(a.Values) == 1 && a.Values[0] == AnswerTerminal && a.Text == ""
}

// resolveInput turns an ask's legacy options or its input into the input
// it is stored with: validated, labels defaulted, checkboxes bounds set.
// Exactly one of options and in must be given.
func resolveInput(options []Option, in *Input) (Input, error) {
	switch {
	case in != nil && len(options) > 0:
		return Input{}, fmt.Errorf("%w: give options or input, not both", ErrInvalid)
	case in == nil && len(options) == 0:
		return Input{}, fmt.Errorf("%w: give an input (or options, for buttons)", ErrInvalid)
	case in == nil:
		return validateInput(Input{Type: InputButtons, Options: options})
	}
	return validateInput(*in)
}

func validateInput(in Input) (Input, error) {
	if utf8.RuneCountInString(in.Placeholder) > MaxPlaceholderRunes {
		return Input{}, fmt.Errorf("%w: placeholder exceeds %d characters", ErrInvalid, MaxPlaceholderRunes)
	}
	if in.Type == InputText {
		if len(in.Options) > 0 || in.AllowText || in.Min != 0 || in.Max != nil {
			return Input{}, fmt.Errorf("%w: a text input takes only a placeholder", ErrInvalid)
		}
		return in, nil
	}
	if in.Type != InputButtons && in.Type != InputRadio && in.Type != InputCheckboxes {
		return Input{}, fmt.Errorf("%w: input type must be buttons, radio, checkboxes or text", ErrInvalid)
	}
	options, err := validateOptions(in.Options)
	if err != nil {
		return Input{}, err
	}
	in.Options = options
	if in.Placeholder != "" && !in.AllowText {
		return Input{}, fmt.Errorf("%w: placeholder needs a text input or allow_text", ErrInvalid)
	}
	if in.Type != InputCheckboxes {
		if in.Min != 0 || in.Max != nil {
			return Input{}, fmt.Errorf("%w: min and max apply to checkboxes only", ErrInvalid)
		}
		return in, nil
	}
	max := len(options)
	if in.Max != nil {
		max = *in.Max
	}
	if in.Min < 0 || max < 1 || in.Min > max || max > len(options) {
		return Input{}, fmt.Errorf("%w: checkboxes need 0 <= min <= max <= %d options, and max at least 1", ErrInvalid, len(options))
	}
	in.Max = &max
	return in, nil
}

// validateOptions returns a copy of options with labels defaulted to the
// value, refusing an empty, oversized or duplicated list.
func validateOptions(options []Option) ([]Option, error) {
	if len(options) == 0 || len(options) > MaxAskOptions {
		return nil, fmt.Errorf("%w: give 1 to %d options", ErrInvalid, MaxAskOptions)
	}
	out := make([]Option, len(options))
	seen := map[string]bool{}
	for i, o := range options {
		if strings.TrimSpace(o.Value) == "" || seen[o.Value] {
			return nil, fmt.Errorf("%w: option values must be non-empty and distinct", ErrInvalid)
		}
		seen[o.Value] = true
		out[i] = o
		if out[i].Label == "" {
			out[i].Label = o.Value
		}
	}
	return out, nil
}

// validAnswerFor checks answer a against input in; see the chat docs for
// the rules per type.
func validAnswerFor(in Input, a Answer) error {
	if utf8.RuneCountInString(a.Text) > MaxAnswerTextRunes {
		return fmt.Errorf("%w: text exceeds %d characters", ErrInvalid, MaxAnswerTextRunes)
	}
	if in.Type == InputText {
		if len(a.Values) > 0 || strings.TrimSpace(a.Text) == "" {
			return fmt.Errorf("%w: answer with text", ErrInvalid)
		}
		return nil
	}
	if a.Text != "" && !in.AllowText {
		return fmt.Errorf("%w: this question does not take text", ErrInvalid)
	}
	known := map[string]bool{}
	for _, o := range in.Options {
		known[o.Value] = true
	}
	seen := map[string]bool{}
	for _, v := range a.Values {
		if !known[v] {
			return fmt.Errorf("%w: %q is not one of the options", ErrInvalid, v)
		}
		if seen[v] {
			return fmt.Errorf("%w: %q is chosen twice", ErrInvalid, v)
		}
		seen[v] = true
	}
	if len(a.Values) == 0 && in.AllowText {
		if a.Text == "" {
			return fmt.Errorf("%w: choose an option or answer with text", ErrInvalid)
		}
		return nil // free text instead of a choice
	}
	switch in.Type {
	case InputCheckboxes:
		max := len(in.Options)
		if in.Max != nil {
			max = *in.Max
		}
		if len(a.Values) < in.Min || len(a.Values) > max {
			return fmt.Errorf("%w: choose %d to %d options", ErrInvalid, in.Min, max)
		}
	default:
		if len(a.Values) != 1 {
			return fmt.Errorf("%w: choose exactly one option", ErrInvalid)
		}
	}
	return nil
}

// labelsOf maps answer values to their option labels, in order.
func labelsOf(in *Input, values []string) []string {
	labels := make([]string, 0, len(values))
	for _, v := range values {
		label := v
		if in != nil {
			for _, o := range in.Options {
				if o.Value == v {
					label = o.Label
				}
			}
		}
		labels = append(labels, label)
	}
	return labels
}
