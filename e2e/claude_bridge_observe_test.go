//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// sseEvent is one event from GET /api/v1/events.
type sseEvent struct {
	Type string
	Data json.RawMessage
}

// eventLog collects the daemon's SSE stream while a test runs.
type eventLog struct {
	mu     sync.Mutex
	events []sseEvent
}

func (l *eventLog) add(ev sseEvent) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []sseEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]sseEvent(nil), l.events...)
}

// streamEvents subscribes to the daemon's SSE stream until ctx ends.
func (s *bridgeE2E) streamEvents(ctx context.Context) *eventLog {
	s.t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/events", s.port), nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		s.t.Fatalf("events: status %d", resp.StatusCode)
	}
	log := &eventLog{}
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		var typ string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				log.add(sseEvent{Type: typ, Data: json.RawMessage(strings.TrimPrefix(line, "data: "))})
			}
		}
	}()
	return log
}

// attentionStep is one published attention reading for an agent, or a
// turn completion marker.
type attentionStep struct {
	turnCompleted bool
	state         observe.AttentionState
	subagents     int
}

func (st attentionStep) String() string {
	if st.turnCompleted {
		return "turn_completed"
	}
	return fmt.Sprintf("%s(sub=%d)", st.state, st.subagents)
}

// attentionSteps is agent's turn completions and attention readings, in
// stream order.
func attentionSteps(events []sseEvent, agent string) []attentionStep {
	var out []attentionStep
	for _, ev := range events {
		switch ev.Type {
		case string(observe.EventAgentTurnCompleted):
			var p observe.AgentTurnCompletedPayload
			if json.Unmarshal(ev.Data, &p) == nil && p.Agent == agent {
				out = append(out, attentionStep{turnCompleted: true})
			}
		case string(observe.EventAgentActivity):
			var p observe.AgentActivityPayload
			if json.Unmarshal(ev.Data, &p) != nil || p.Agent != agent || p.Attention == nil {
				continue
			}
			step := attentionStep{state: p.Attention.State}
			if p.Attention.Outstanding != nil {
				step.subagents = p.Attention.Outstanding.Subagents
			}
			out = append(out, step)
		}
	}
	return out
}

// b051Verdict checks steps for the B-051 shape: once a turn completes
// with a subagent running, the agent's next finished must come with nothing
// outstanding: a finished while the subagent still counts is the bug. done
// reports whether that finished has arrived.
func b051Verdict(steps []attentionStep) (done bool, err error) {
	subagents := 0
	for i, st := range steps {
		if !st.turnCompleted {
			subagents = st.subagents
			continue
		}
		if subagents == 0 {
			continue
		}
		for _, next := range steps[i+1:] {
			if next.turnCompleted || next.state != observe.AttentionFinished {
				continue
			}
			if next.subagents != 0 {
				return false, fmt.Errorf("finished with %d subagents outstanding (B-051): %v", next.subagents, steps)
			}
			return true, nil
		}
		return false, nil
	}
	return false, nil
}

// TestClaudeBridgeBackgroundSubagentHoldsWorking is the B-051 repro: a
// turn starts a background subagent and ends; the agent must read working
// with outstanding.subagents until SubagentStop, then finished.
func TestClaudeBridgeBackgroundSubagentHoldsWorking(t *testing.T) {
	s := newBridgeE2E(t, bridgeOptions{})
	const name = "b051-e2e"
	s.spawn(name, "Reply with exactly: READY")
	s.awaitBridge(name, agent.BridgeConnected)
	s.transcript(name).await("the opening reply", said("READY"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := s.streamEvents(ctx)

	prompt := `Use the Agent tool with run_in_background set to true to start one general-purpose subagent. ` +
		`Its task: use the Bash tool to run exactly: python3 -c "import time; time.sleep(30)"   then reply DONE. ` +
		`Do not wait for it. Right after starting it, reply with exactly: SPAWNED`
	if code, body := s.message(name, prompt, ""); code/100 != 2 {
		t.Fatalf("message: %d %s", code, body)
	}

	deadline := time.Now().Add(2 * bridgeTurnTimeout)
	for time.Now().Before(deadline) {
		done, err := b051Verdict(attentionSteps(log.snapshot(), name))
		if err != nil {
			t.Fatal(err)
		}
		if done {
			t.Logf("attention steps: %v", attentionSteps(log.snapshot(), name))
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("no held-then-finished sequence; steps: %v", attentionSteps(log.snapshot(), name))
}
