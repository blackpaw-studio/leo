package consult

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/claude"
)

func TestUsageFinalReplacesProvisionalAndInvocationsAggregate(t *testing.T) {
	d := NewDispatcher(nopRecorder{})
	state := &runState{record: Record{ID: "d-usage"}, handle: nopHandle{}}
	d.applyUsageLocked(state, &harness.Usage{InputTokens: harness.Int64(4), OutputTokens: harness.Int64(99), ToolCalls: harness.Int(1)}, false)
	d.applyUsageLocked(state, &harness.Usage{InputTokens: harness.Int64(5), OutputTokens: harness.Int64(2), ToolCalls: harness.Int(1)}, true)
	if *state.record.InputTokens != 5 || *state.record.OutputTokens != 2 || len(state.record.UsageInvocations) != 1 || !state.record.UsageInvocations[0].Completed {
		t.Fatalf("final did not replace provisional: %+v", state.record)
	}
	d.beginUsageInvocationLocked(state)
	d.applyUsageLocked(state, &harness.Usage{InputTokens: harness.Int64(3), OutputTokens: harness.Int64(4), ToolCalls: harness.Int(0)}, true)
	if *state.record.InputTokens != 8 || *state.record.OutputTokens != 6 || *state.record.ToolCalls != 1 {
		t.Fatalf("invocations did not aggregate: %+v", state.record)
	}
}

func TestRecordingTeeFeedsOnlyCompleteLinesAndAgreesWithFinalParser(t *testing.T) {
	acc := claude.Claude{}.NewUsageAccumulator()
	var snapshots []*harness.Usage
	tee := &recordingTee{handle: nopHandle{}, usage: acc, onUsage: func(u *harness.Usage) { snapshots = append(snapshots, u) }}
	line := []byte(`{"type":"assistant","message":{"id":"m","usage":{"input_tokens":7},"content":[{"type":"tool_use","id":"t"}]}}`)
	if _, err := tee.Write(line[:20]); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("partial native line was consumed: %#v", snapshots)
	}
	if _, err := tee.Write(append(line[20:], '\n')); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || *snapshots[0].InputTokens != 7 || *snapshots[0].ToolCalls != 1 {
		t.Fatalf("live usage=%#v", snapshots)
	}
	final, err := (claude.Claude{}).ParseEvents(bytes.NewReader(tee.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if final.Usage == nil || *final.Usage.InputTokens != *snapshots[0].InputTokens || *final.Usage.ToolCalls != *snapshots[0].ToolCalls {
		t.Fatalf("live=%#v final=%#v", snapshots[0], final.Usage)
	}
}

func TestFailedInvocationPreservesMeasuredUsage(t *testing.T) {
	d := NewDispatcher(nopRecorder{})
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' '{"type":"result","result":"failed","is_error":true,"usage":{"input_tokens":3,"output_tokens":0},"total_cost_usd":0,"num_turns":1}'; exit 1`)
	}
	state := &runState{record: Record{ID: "d-failed", Status: StatusQueued}, handle: nopHandle{}, done: make(chan struct{})}
	d.run(context.Background(), state, claude.Claude{}, "sonnet", nil, nil, nil, ".", 0)
	if state.record.Status != StatusFailed || state.record.InputTokens == nil || *state.record.InputTokens != 3 || state.record.OutputTokens == nil || *state.record.OutputTokens != 0 || state.record.CostUSD == nil || *state.record.CostUSD != 0 {
		t.Fatalf("failed run lost usage: %+v", state.record)
	}
}

func TestCanceledInvocationPreservesMeasuredUsage(t *testing.T) {
	d := NewDispatcher(nopRecorder{})
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' '{"type":"result","result":"partial","usage":{"input_tokens":4,"output_tokens":0},"total_cost_usd":0,"num_turns":1}'; sleep 5`)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &fakeHandle{}
	state := &runState{record: Record{ID: "d-canceled", Status: StatusQueued}, handle: handle, done: make(chan struct{})}
	go d.run(ctx, state, claude.Claude{}, "sonnet", nil, nil, nil, ".", 0)
	deadline := time.Now().Add(time.Second)
	for {
		handle.mu.Lock()
		measured := handle.latest.InputTokens != nil && *handle.latest.InputTokens == 4
		handle.mu.Unlock()
		if measured {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("usage was not recorded before cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-state.done
	if state.record.Status != StatusCanceled || state.record.InputTokens == nil || *state.record.InputTokens != 4 || state.record.OutputTokens == nil || *state.record.OutputTokens != 0 || state.record.CostUSD == nil || *state.record.CostUSD != 0 {
		t.Fatalf("canceled run lost usage: %+v", state.record)
	}
}
