package consult

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// gatedCloseRecorder hands out handles whose Close blocks until released, so
// a test can observe the window between a run turning terminal and its
// recording being finalized.
type gatedCloseRecorder struct {
	entered chan struct{}
	release chan struct{}
}

func (r *gatedCloseRecorder) Open(Record) (Handle, error) {
	return gatedCloseHandle{nopHandle: nopHandle{}, r: r}, nil
}
func (r *gatedCloseRecorder) Resume(rec Record) (Handle, error) { return r.Open(rec) }

type gatedCloseHandle struct {
	nopHandle
	r *gatedCloseRecorder
}

func (h gatedCloseHandle) Close(Status, error) error {
	close(h.r.entered)
	<-h.r.release
	return nil
}

func TestWaitDoesNotReturnUntilTerminalRecordingIsFinalized(t *testing.T) {
	rec := &gatedCloseRecorder{entered: make(chan struct{}), release: make(chan struct{})}
	d := NewDispatcher(rec)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", `{"type":"result","result":"ok","is_error":false}`)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), Kind: "dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-rec.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run never reached recording finalization")
	}

	returned := make(chan []Entry, 1)
	go func() { returned <- d.Wait(context.Background(), []string{started.ID}, 5*time.Second) }()
	select {
	case <-returned:
		t.Fatal("Wait returned while the terminal recording was still being written")
	case <-time.After(100 * time.Millisecond):
	}
	close(rec.release)
	select {
	case entries := <-returned:
		if entries[0].Status != StatusDone {
			t.Fatalf("entry = %+v", entries[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after finalization")
	}
}
