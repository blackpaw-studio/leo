package startup

import (
	"context"
	"os/exec"
	"testing"
)

func TestExecCounterClassifiesByBaseName(t *testing.T) {
	var counter ExecCounter
	var ran []string
	inner := func(_ context.Context, name string, _ ...string) *exec.Cmd {
		ran = append(ran, name)
		return nil
	}
	wrapped := counter.WrapContext(inner)
	for _, name := range []string{"/opt/homebrew/bin/tmux", "git", "sh", "tmux"} {
		wrapped(context.Background(), name)
	}
	if got, want := counter.Snapshot(), (ExecCounts{Tmux: 2, Git: 1}); got != want {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}
	if len(ran) != 4 {
		t.Fatalf("inner ran %d times, want 4", len(ran))
	}

	plain := counter.Wrap(func(name string, _ ...string) *exec.Cmd { return nil })
	plain("/usr/bin/git", "status")
	if got := counter.Snapshot().Git; got != 2 {
		t.Fatalf("Git after Wrap = %d, want 2", got)
	}
}
