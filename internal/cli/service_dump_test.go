package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
)

// dumpFixture fakes the daemon, the signal and the state dir. Sleeping
// advances a fake clock, so no test waits in real time.
type dumpFixture struct {
	info     daemon.DumpInfo
	running  bool
	files    []string
	appears  string // file the signal makes appear ("" = none ever)
	signaled []int
	clock    time.Time
}

func (f *dumpFixture) deps() serviceDumpDeps {
	return serviceDumpDeps{
		target: func(context.Context, string) (daemon.DumpInfo, bool) { return f.info, f.running },
		signal: func(pid int) error {
			f.signaled = append(f.signaled, pid)
			if f.appears != "" {
				f.files = append(f.files, f.appears)
			}
			return nil
		},
		glob:    func(string) ([]string, error) { return append([]string(nil), f.files...), nil },
		now:     func() time.Time { return f.clock },
		sleep:   func(d time.Duration) { f.clock = f.clock.Add(d) },
		timeout: 10 * time.Second,
	}
}

func runDump(f *dumpFixture) (string, error) {
	var out bytes.Buffer
	err := runServiceDump(context.Background(), "/home/leo", &out, f.deps())
	return out.String(), err
}

func TestServiceDumpDaemonNotRunning(t *testing.T) {
	f := &dumpFixture{running: false}

	_, err := runDump(f)

	if err == nil || !strings.Contains(err.Error(), "daemon is not running (no answer on /home/leo/state/leo.sock)") {
		t.Fatalf("err = %v", err)
	}
	if len(f.signaled) != 0 {
		t.Errorf("signalled %v with no daemon answering", f.signaled)
	}
}

func TestServiceDumpRefusesDaemonWithoutHandler(t *testing.T) {
	f := &dumpFixture{running: true, info: daemon.DumpInfo{PID: 4242, Version: "v0.58.0"}}

	_, err := runDump(f)

	want := "daemon pid 4242 (v0.58.0) predates 'leo service dump'; not signalled (SIGUSR1 would terminate it)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if len(f.signaled) != 0 {
		t.Errorf("signalled %v: SIGUSR1 would have killed the old daemon", f.signaled)
	}
}

func TestServiceDumpPrintsTheNewFile(t *testing.T) {
	f := &dumpFixture{
		running: true, info: daemon.DumpInfo{PID: 4242, Version: "v0.59.0", Capable: true},
		files:   []string{"/home/leo/state/goroutines-1.txt"},
		appears: "/home/leo/state/goroutines-2.txt",
	}

	out, err := runDump(f)

	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "/home/leo/state/goroutines-2.txt" {
		t.Errorf("output = %q, want only the new dump's path", out)
	}
	if len(f.signaled) != 1 || f.signaled[0] != 4242 {
		t.Errorf("signalled %v, want [4242]", f.signaled)
	}
}

func TestServiceDumpTimesOutWhenNoFileAppears(t *testing.T) {
	f := &dumpFixture{running: true, info: daemon.DumpInfo{PID: 4242, Version: "v0.59.0", Capable: true}}

	_, err := runDump(f)

	want := "signalled pid 4242 but no dump appeared in 10s; see 'leo service logs'"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
