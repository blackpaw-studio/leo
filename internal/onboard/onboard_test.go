package onboard

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSmokeTestSuccess(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		// Verify the right command is being built
		if name != "claude" {
			t.Errorf("command = %q, want %q", name, "claude")
		}
		// Use a real command that echoes the expected output
		return exec.Command("echo", "LEO_SMOKE_OK")
	}

	err := SmokeTest()
	if err != nil {
		t.Fatalf("SmokeTest() error: %v", err)
	}
}

func TestSmokeTestBadOutput(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "something else")
	}

	err := SmokeTest()
	if err == nil {
		t.Fatal("expected error for bad output")
	}

	if !strings.Contains(err.Error(), "unexpected") {
		t.Errorf("error = %q, want to contain 'unexpected'", err.Error())
	}
}

func TestSmokeTestCommandFailure(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	err := SmokeTest()
	if err == nil {
		t.Fatal("expected error for command failure")
	}

	if !strings.Contains(err.Error(), "smoke test failed") {
		t.Errorf("error = %q, want to contain 'smoke test failed'", err.Error())
	}
}

func TestSmokeTestArgs(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	var gotArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotArgs = args
		return exec.Command("echo", "LEO_SMOKE_OK")
	}

	SmokeTest()

	expected := []string{"-p", "Reply with exactly: LEO_SMOKE_OK", "--max-turns", "1", "--output-format", "text"}
	if len(gotArgs) != len(expected) {
		t.Fatalf("args count = %d, want %d", len(gotArgs), len(expected))
	}

	for i, arg := range expected {
		if gotArgs[i] != arg {
			t.Errorf("args[%d] = %q, want %q", i, gotArgs[i], arg)
		}
	}
}
