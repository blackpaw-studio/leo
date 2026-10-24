package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var dumpNow = time.Unix(1_800_000_000, 0)

func newTestDumper(t *testing.T) (goroutineDumper, *[]string) {
	t.Helper()
	var logs []string
	return goroutineDumper{
		dir:  t.TempDir(),
		now:  func() time.Time { return dumpNow },
		logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	}, &logs
}

func TestDumpSignalWritesGoroutineFile(t *testing.T) {
	dumper, logs := newTestDumper(t)
	sigs := make(chan os.Signal, 1)
	done := make(chan struct{})
	dumped := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		serveDumpSignals(sigs, done, func() { _, _ = dumper.dump(); dumped <- struct{}{} })
		close(finished)
	}()

	sigs <- os.Interrupt // any signal on the channel triggers a dump
	<-dumped
	close(done)
	<-finished

	want := filepath.Join(dumper.dir, "goroutines-1800000000.txt")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("dump file missing: %v", err)
	}
	if !strings.Contains(string(body), "goroutine ") {
		t.Errorf("dump lacks goroutine stacks: %.200s", body)
	}
	if info, _ := os.Stat(want); info.Mode().Perm() != 0o600 {
		t.Errorf("dump mode = %v, want 0600", info.Mode().Perm())
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "goroutine dump written to "+want) {
		t.Errorf("logs = %q, want one line naming %s", *logs, want)
	}
	if left, _ := filepath.Glob(filepath.Join(dumper.dir, ".goroutines-*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestDumpSameSecondGetsSuffix(t *testing.T) {
	dumper, _ := newTestDumper(t)

	first, err := dumper.dump()
	if err != nil {
		t.Fatal(err)
	}
	second, err := dumper.dump()
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Base(first) != "goroutines-1800000000.txt" || filepath.Base(second) != "goroutines-1800000000-2.txt" {
		t.Errorf("names = %s, %s", filepath.Base(first), filepath.Base(second))
	}
}
