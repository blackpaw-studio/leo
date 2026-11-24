//go:build darwin

package credentials

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecRunnerReportsStdoutAndExitCodesWithoutAShell(t *testing.T) {
	out, code, err := execRunner(context.Background(), "/bin/echo", "hi; echo injected")
	if err != nil || code != 0 || string(out) != "hi; echo injected\n" {
		t.Fatalf("echo = %q, %d, %v", out, code, err)
	}
	if _, code, err := execRunner(context.Background(), "/usr/bin/false"); err != nil || code != 1 {
		t.Fatalf("false = %d, %v; want exit 1 and no error", code, err)
	}
}

func TestExecRunnerEndsAtTheContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := execRunner(ctx, "/bin/sleep", "30")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}
