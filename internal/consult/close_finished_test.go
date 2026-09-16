package consult

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func closeFinishedDispatcher(t *testing.T) (*Dispatcher, *fakeInteractiveRuntime) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{alive: true}
	d.SetInteractiveRuntime(rt)
	return d, rt
}
func addCloseRecord(d *Dispatcher, id string, mode Mode, status Status, session, window string) {
	d.runs[id] = &runState{record: Record{ID: id, Kind: "dispatch", Mode: mode, Status: status, PaneID: "%" + id, ViewerPaneID: "%" + id, ViewerKind: "split", CallerSessionID: session, CallerWindowID: window}, handle: nopHandle{}, done: make(chan struct{})}
}

func TestCloseFinishedStates(t *testing.T) {
	d, _ := closeFinishedDispatcher(t)
	addCloseRecord(d, "idle", ModeInteractive, StatusIdle, "$1", "@1")
	addCloseRecord(d, "running", ModeInteractive, StatusRunning, "$1", "@1")
	addCloseRecord(d, "timeout", ModeInteractive, StatusTimeout, "$1", "@1")
	got, err := d.CloseFinished("$1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || d.runs["running"].record.Status != StatusRunning {
		t.Fatalf("got=%+v", got)
	}
}
func TestCloseFinishedSerializesWithSend(t *testing.T) {
	d, rt := closeFinishedDispatcher(t)
	addCloseRecord(d, "idle", ModeInteractive, StatusIdle, "$1", "@1")
	entered, releaseSend := make(chan struct{}), make(chan struct{})
	rt.arm = true
	rt.injectHook = func() { close(entered); <-releaseSend }
	var wg sync.WaitGroup
	wg.Add(2)
	var closeErr, sendErr error
	go func() { defer wg.Done(); _, sendErr = d.Send(context.Background(), "idle", "continue") }()
	<-entered
	closeDone := make(chan struct{})
	go func() { defer wg.Done(); defer close(closeDone); _, closeErr = d.CloseFinished("$1") }()
	select {
	case <-closeDone:
		t.Fatal("CloseFinished passed Send inside serialized section")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSend)
	wg.Wait()
	if closeErr != nil || sendErr != nil || d.runs["idle"].record.Status != StatusQueued {
		t.Fatalf("close=%v send=%v status=%s", closeErr, sendErr, d.runs["idle"].record.Status)
	}
}

func TestCloseFinishedAppliesLayoutsAfterCloseFailure(t *testing.T) {
	d, rt := closeFinishedDispatcher(t)
	addCloseRecord(d, "a", ModeHeadless, StatusDone, "$1", "@1")
	addCloseRecord(d, "b", ModeHeadless, StatusDone, "$1", "@2")
	d.SetCloseFinishedViewer(func(rec Record, layout func(string) error) (Record, error) {
		if rec.ID == "a" {
			_ = layout("@1")
			return rec, nil
		}
		return rec, errors.New("kill failed")
	})
	if _, err := d.CloseFinished("$1"); err == nil {
		t.Fatal("want joined error")
	}
	if !reflect.DeepEqual(rt.layouts, []string{"@1"}) {
		t.Fatalf("layouts=%q", rt.layouts)
	}
}

func TestCloseFinishedAttemptsEveryLayout(t *testing.T) {
	d, rt := closeFinishedDispatcher(t)
	rt.layoutErr = map[string]error{"@1": errors.New("layout failed")}
	addCloseRecord(d, "a", ModeHeadless, StatusDone, "$1", "@1")
	addCloseRecord(d, "b", ModeHeadless, StatusDone, "$1", "@2")
	d.SetCloseFinishedViewer(func(rec Record, layout func(string) error) (Record, error) {
		_ = layout(rec.CallerWindowID)
		return rec, nil
	})
	if _, err := d.CloseFinished("$1"); err == nil {
		t.Fatal("want layout error")
	}
	if !reflect.DeepEqual(rt.layouts, []string{"@1", "@2"}) {
		t.Fatalf("layouts=%q", rt.layouts)
	}
}
func TestCloseFinishedLayoutsOncePerWindow(t *testing.T) {
	d, rt := closeFinishedDispatcher(t)
	addCloseRecord(d, "a", ModeInteractive, StatusIdle, "$1", "@1")
	addCloseRecord(d, "b", ModeInteractive, StatusIdle, "$1", "@1")
	if _, err := d.CloseFinished("$1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rt.layouts, []string{"@1"}) {
		t.Fatalf("layouts=%q", rt.layouts)
	}
}
func TestCloseFinishedKillFailure(t *testing.T) {
	d, rt := closeFinishedDispatcher(t)
	addCloseRecord(d, "a", ModeInteractive, StatusIdle, "$1", "@1")
	rt.killErr = errors.New("boom")
	if _, err := d.CloseFinished("$1"); err == nil {
		t.Fatal("want error")
	}
	if d.runs["a"].record.PaneID == "" {
		t.Fatal("pane lost after failed kill")
	}
}
