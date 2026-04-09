package onboard

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/prereq"
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

func TestRunClaudeNotFound(t *testing.T) {
	origCheck := checkClaudeFn
	origSetup := setupInteractiveFn
	origReader := newReaderFn
	t.Cleanup(func() {
		checkClaudeFn = origCheck
		setupInteractiveFn = origSetup
		newReaderFn = origReader
	})

	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: false}
	}
	setupInteractiveFn = func(r *bufio.Reader) error { return nil }

	err := Run()
	if err == nil {
		t.Fatal("expected error when claude not found")
	}
	if !strings.Contains(err.Error(), "claude CLI not found") {
		t.Errorf("error = %q, want to contain 'claude CLI not found'", err.Error())
	}
}

func TestRunNothingFound(t *testing.T) {
	origCheck := checkClaudeFn
	origSetup := setupInteractiveFn
	origReader := newReaderFn
	t.Cleanup(func() {
		checkClaudeFn = origCheck
		setupInteractiveFn = origSetup
		newReaderFn = origReader
	})

	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Version: "1.0.0"}
	}

	var setupCalled bool
	setupInteractiveFn = func(r *bufio.Reader) error {
		setupCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !setupCalled {
		t.Error("expected setupInteractiveFn to be called")
	}
}

func TestRunClaudeVersionEmpty(t *testing.T) {
	origCheck := checkClaudeFn
	origSetup := setupInteractiveFn
	origReader := newReaderFn
	t.Cleanup(func() {
		checkClaudeFn = origCheck
		setupInteractiveFn = origSetup
		newReaderFn = origReader
	})

	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Version: ""}
	}
	setupInteractiveFn = func(r *bufio.Reader) error { return nil }

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
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

	_ = SmokeTest()

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
