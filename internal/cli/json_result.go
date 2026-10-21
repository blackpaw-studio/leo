package cli

import (
	"encoding/json"
	"errors"
	"io"
)

// errJSONReported marks a failure already written to stdout as JSON. Its
// empty message makes main print nothing more and exit non-zero.
var errJSONReported = errors.New("")

// jsonError is a failure with a stable, machine-readable code. Payload, when
// set, is printed instead of the bare {error, message} object (handover's
// full result carries the code itself).
type jsonError struct {
	Code    string
	Message string
	Payload any
}

func (e *jsonError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

// jsonFailure is the shape of every --json failure.
type jsonFailure struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// failJSON builds a coded failure from an underlying error (may be nil).
func failJSON(code string, err error) *jsonError {
	e := &jsonError{Code: code}
	if err != nil {
		e.Message = err.Error()
	}
	return e
}

// runJSON runs f and writes exactly one JSON object to out: f's result, or
// the failure as {error, message?}. A failure also returns errJSONReported so
// the process exits non-zero without a second message. Everything else the
// command prints must go to stderr.
func runJSON(out io.Writer, f func() (any, error)) error {
	v, err := f()
	if err == nil {
		return writeJSON(out, v)
	}
	var je *jsonError
	if !errors.As(err, &je) {
		je = failJSON("internal", err)
	}
	payload := je.Payload
	if payload == nil {
		payload = jsonFailure{Error: je.Code, Message: je.Message}
	}
	if werr := writeJSON(out, payload); werr != nil {
		return werr
	}
	return errJSONReported
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	return enc.Encode(v)
}

// asJSONError unwraps err to a *jsonError.
func asJSONError(err error, target **jsonError) bool { return errors.As(err, target) }
