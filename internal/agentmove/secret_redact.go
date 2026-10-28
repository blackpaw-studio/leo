package agentmove

import (
	"errors"

	"github.com/blackpaw-studio/leo/internal/redact"
)

// redactError is err with the agent's secret values replaced in its
// message, for a hook's or harness command's output that reached it. An
// error that holds no value comes back as is. One that does still reads as
// its original's code and verdict (CodeOf, HasCode, Definite) and matches
// what errors.Is matched, but errors.As only ever finds redacted text.
func redactError(s *redact.Secrets, err error) error {
	if err == nil {
		return nil
	}
	msg := s.String(err.Error())
	if msg == err.Error() {
		return err
	}
	out := &redactedError{msg: msg, cause: err}
	var p *Problem
	if errors.As(err, &p) {
		out.problem = &Problem{Code: p.Code, Message: s.String(p.Message)}
	}
	var ve *VerbError
	if errors.As(err, &ve) {
		out.verb = &VerbError{Code: ve.Code, Message: s.String(ve.Message)}
	}
	var ps Problems
	if errors.As(err, &ps) {
		out.problems = redactProblems(s, ps)
	}
	return out
}

func redactProblems(s *redact.Secrets, ps []Problem) []Problem {
	if ps == nil {
		return nil
	}
	out := make([]Problem, len(ps))
	for i, p := range ps {
		out[i] = Problem{Code: p.Code, Message: s.String(p.Message)}
	}
	return out
}

type redactedError struct {
	msg      string
	cause    error
	problem  *Problem
	verb     *VerbError
	problems Problems
}

func (e *redactedError) Error() string { return e.msg }

// Is matches what the original matched; it reveals no text.
func (e *redactedError) Is(target error) bool { return errors.Is(e.cause, target) }

func (e *redactedError) As(target any) bool {
	switch t := target.(type) {
	case **Problem:
		*t = e.problem
		return e.problem != nil
	case **VerbError:
		*t = e.verb
		return e.verb != nil
	case *Problems:
		*t = e.problems
		return e.problems != nil
	}
	return false
}
