package cli

import (
	"bytes"
	"errors"
	"testing"
)

func TestRunJSONWritesTheResultAsTheOnlyOutput(t *testing.T) {
	var out bytes.Buffer

	err := runJSON(&out, func() (any, error) { return map[string]any{"ok": true}, nil })

	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "{\"ok\":true}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunJSONFailureIsACodedObjectAndASilentNonZeroExit(t *testing.T) {
	var out bytes.Buffer

	err := runJSON(&out, func() (any, error) { return nil, failJSON("not_registered", errors.New("job missing")) })

	if got, want := out.String(), "{\"error\":\"not_registered\",\"message\":\"job missing\"}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if err == nil || err.Error() != "" || ExitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want a silent exit 1", err, ExitCode(err))
	}
}

func TestRunJSONUncodedErrorsAreInternal(t *testing.T) {
	var out bytes.Buffer

	_ = runJSON(&out, func() (any, error) { return nil, errors.New("boom") })

	if got, want := out.String(), "{\"error\":\"internal\",\"message\":\"boom\"}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunJSONFailureMayCarryAFullPayload(t *testing.T) {
	var out bytes.Buffer
	payload := map[string]any{"ok": false, "error": "x", "step": "bootout"}

	_ = runJSON(&out, func() (any, error) { return nil, &jsonError{Code: "x", Payload: payload} })

	if got, want := out.String(), "{\"error\":\"x\",\"ok\":false,\"step\":\"bootout\"}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
