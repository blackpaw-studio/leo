package clienttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// recordingPublisher captures published events and signals each one, so tests
// wait on events instead of sleeping.
type recordingPublisher struct {
	mu     sync.Mutex
	events []observe.Event
	signal chan observe.Event
}

func newRecordingPublisher() *recordingPublisher {
	return &recordingPublisher{signal: make(chan observe.Event, 64)}
}

func (p *recordingPublisher) Publish(ev observe.Event) {
	p.mu.Lock()
	p.events = append(p.events, ev)
	p.mu.Unlock()
	p.signal <- ev
}

// next waits for the next published event of the given type.
func (p *recordingPublisher) next(t *testing.T, typ observe.EventType) observe.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-p.signal:
			if ev.Type == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

// fakeClock hands out controllable timers.
type fakeClock struct {
	mu     sync.Mutex
	waits  []time.Duration
	timers []chan time.Time
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waits = append(c.waits, d)
	c.timers = append(c.timers, ch)
	return ch
}

func (c *fakeClock) fire(i int) { c.timers[i] <- time.Time{} }

func (c *fakeClock) waited() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

type callFixture struct {
	reg   *Registry
	calls *Calls
	pub   *recordingPublisher
	clock *fakeClock
	a, b  ClientID
}

// newCallFixture registers a "browser" group (browser_click: 30 s,
// browser_handoff: no timeout) provided by two clients.
func newCallFixture(t *testing.T) *callFixture {
	t.Helper()
	return newCallFixtureWith(t, func(*CallsOptions) {})
}

// newCallFixtureWith is newCallFixture with the call tracker's options adjusted.
func newCallFixtureWith(t *testing.T, adjust func(*CallsOptions)) *callFixture {
	t.Helper()
	reg := newTestRegistry()
	f := &callFixture{reg: reg, pub: newRecordingPublisher(), clock: &fakeClock{}}
	f.a, f.b = reg.Connect(), reg.Connect()
	handoff := tool("browser_handoff")
	handoff.TimeoutS = 0
	tools := []Tool{tool("browser_click"), handoff}
	for _, id := range []ClientID{f.a, f.b} {
		if _, _, err := reg.Register(id, "browser", tools); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	opts := CallsOptions{
		Registry: reg,
		Publish:  f.pub,
		After:    f.clock.After,
		Now:      func() time.Time { return time.Date(2026, 10, 26, 15, 21, 2, 123_456_789, time.UTC) },
		NewRequestID: func() string {
			n++
			return fmt.Sprintf("ctr_%026d", n)
		},
	}
	adjust(&opts)
	f.calls = NewCalls(opts)
	return f
}

type callOutcome struct {
	res Result
	err error
}

// start runs a call in the background and returns the request it published.
func (f *callFixture) start(t *testing.T, ctx context.Context, agent, toolName string) (<-chan callOutcome, observe.ClientToolRequest) {
	t.Helper()
	out := make(chan callOutcome, 1)
	go func() {
		res, err := f.calls.Call(ctx, agent, toolName, json.RawMessage(`{"ref":"e1"}`))
		out <- callOutcome{res, err}
	}()
	ev := f.pub.next(t, observe.EventClientToolRequest)
	return out, ev.Payload.(*observe.ClientToolRequestPayload).Request
}

func wait(t *testing.T, ch <-chan callOutcome) callOutcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return")
		return callOutcome{}
	}
}

func okResult(content string) Result {
	return Result{OK: true, Content: json.RawMessage(content)}
}

func TestCallPublishesParkedRequest(t *testing.T) {
	f := newCallFixture(t)
	_, req := f.start(t, context.Background(), "alfred", "browser_click")
	want := observe.ClientToolRequest{
		ID: "ctr_00000000000000000000000001", Agent: "alfred", Group: "browser", Tool: "browser_click",
		Args: json.RawMessage(`{"ref":"e1"}`), TimeoutS: 30,
		CreatedAt: time.Date(2026, 10, 26, 15, 21, 2, 123_000_000, time.UTC),
	}
	if req.ID != want.ID || req.Agent != want.Agent || req.Group != want.Group || req.Tool != want.Tool ||
		req.TimeoutS != want.TimeoutS || !req.CreatedAt.Equal(want.CreatedAt) || string(req.Args) != string(want.Args) || req.Owner != "" {
		t.Fatalf("request = %+v, want %+v", req, want)
	}
}

func TestCallTimesOutPerTool(t *testing.T) {
	f := newCallFixture(t)
	out, req := f.start(t, context.Background(), "alfred", "browser_click")
	if got := f.clock.waited(); len(got) != 1 || got[0] != 30*time.Second {
		t.Fatalf("waited = %v, want [30s]", got)
	}
	f.clock.fire(0)
	o := wait(t, out)
	if o.err != nil || o.res.OK || o.res.Error == nil || o.res.Error.Code != "timeout" {
		t.Fatalf("outcome = %+v", o)
	}
	cancelled := f.pub.next(t, observe.EventClientToolCancelled).Payload.(*observe.ClientToolCancelledPayload)
	if cancelled.ID != req.ID || cancelled.Reason != observe.CancelTimeout || cancelled.Agent != "alfred" {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	if len(f.calls.Pending()) != 0 {
		t.Fatal("timed-out request still pending")
	}
	if err := f.calls.Settle(req.ID, f.a, okResult(`[]`)); !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("late result err = %v, want ErrAlreadySettled", err)
	}
}

func TestTimeoutZeroWaitsForCtx(t *testing.T) {
	f := newCallFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	out, _ := f.start(t, ctx, "alfred", "browser_handoff")
	if got := f.clock.waited(); len(got) != 0 {
		t.Fatalf("a timeout-0 tool armed a timer: %v", got)
	}
	cancel()
	o := wait(t, out)
	if o.res.OK || o.res.Error == nil || o.res.Error.Code != "cancelled" {
		t.Fatalf("outcome = %+v", o)
	}
	if c := f.pub.next(t, observe.EventClientToolCancelled).Payload.(*observe.ClientToolCancelledPayload); c.Reason != observe.CancelCancelled {
		t.Fatalf("reason = %q", c.Reason)
	}
}

func TestTimeoutZeroIsBoundedByTheDaemonCeiling(t *testing.T) {
	// A harness kills a tool call at its own ceiling and may never tell the
	// daemon, so a no-timeout tool must not stay parked past it.
	const ceiling = 31 * time.Minute
	f := newCallFixtureWith(t, func(o *CallsOptions) { o.MaxWait = ceiling })
	out, req := f.start(t, context.Background(), "alfred", "browser_handoff")
	if got := f.clock.waited(); len(got) != 1 || got[0] != ceiling {
		t.Fatalf("waited = %v, want [%s]", got, ceiling)
	}
	f.clock.fire(0)
	o := wait(t, out)
	if o.res.OK || o.res.Error == nil || o.res.Error.Code != "timeout" {
		t.Fatalf("outcome = %+v", o)
	}
	cancelled := f.pub.next(t, observe.EventClientToolCancelled).Payload.(*observe.ClientToolCancelledPayload)
	if cancelled.ID != req.ID || cancelled.Reason != observe.CancelTimeout {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	if len(f.calls.Pending()) != 0 {
		t.Fatal("timed-out request still pending")
	}
}

func TestToolTimeoutLongerThanTheCeilingIsCapped(t *testing.T) {
	f := newCallFixtureWith(t, func(o *CallsOptions) { o.MaxWait = 10 * time.Second })
	f.start(t, context.Background(), "alfred", "browser_click")
	if got := f.clock.waited(); len(got) != 1 || got[0] != 10*time.Second {
		t.Fatalf("waited = %v, a 30 s tool must not outlive the ceiling", got)
	}
}

func TestNoClientFailsFast(t *testing.T) {
	f := newCallFixture(t)
	f.reg.Disconnect(f.a)
	f.reg.Disconnect(f.b)
	res, err := f.calls.Call(context.Background(), "alfred", "browser_click", nil)
	if err != nil || res.OK || res.Error == nil || res.Error.Code != "no_client" {
		t.Fatalf("withdrawn tool: res=%+v err=%v", res, err)
	}
	if _, err := f.calls.Call(context.Background(), "alfred", "never_registered", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("unknown tool err = %v, want ErrUnknownTool", err)
	}
	if len(f.calls.Pending()) != 0 {
		t.Fatal("a failed-fast call was parked")
	}
}

func TestOwnershipFirstClaimWins409(t *testing.T) {
	f := newCallFixture(t)
	out1, req1 := f.start(t, context.Background(), "alfred", "browser_handoff")

	claimed, err := f.calls.Claim(req1.ID, f.b)
	if err != nil || claimed.Owner != string(f.b) {
		t.Fatalf("first claim: %+v %v", claimed, err)
	}
	if _, err := f.calls.Claim(req1.ID, f.a); !errors.Is(err, ErrAlreadyClaimed) && !errors.Is(err, ErrOwnedByOther) {
		t.Fatalf("second claimant err = %v", err)
	}
	// The claimant re-claiming (after a reconnect) attaches instead of failing.
	if again, err := f.calls.Claim(req1.ID, f.b); err != nil || again.ID != req1.ID {
		t.Fatalf("re-claim: %+v %v", again, err)
	}

	// The next request for the same (agent, group) carries the owner and only it may claim.
	out2, req2 := f.start(t, context.Background(), "alfred", "browser_handoff")
	if req2.Owner != string(f.b) {
		t.Fatalf("second request owner = %q, want %q", req2.Owner, f.b)
	}
	if _, err := f.calls.Claim(req2.ID, f.a); !errors.Is(err, ErrOwnedByOther) {
		t.Fatalf("non-owner claim err = %v, want ErrOwnedByOther", err)
	}

	// Ownership is per agent: another agent's request goes to whoever claims first.
	out3, req3 := f.start(t, context.Background(), "bob", "browser_handoff")
	if req3.Owner != "" {
		t.Fatalf("bob's request owner = %q", req3.Owner)
	}
	if _, err := f.calls.Claim(req3.ID, f.a); err != nil {
		t.Fatalf("a claims bob's request: %v", err)
	}

	for _, p := range []struct {
		id  string
		by  ClientID
		out <-chan callOutcome
	}{{req1.ID, f.b, out1}, {req2.ID, f.b, out2}, {req3.ID, f.a, out3}} {
		if p.id == req2.ID {
			if _, err := f.calls.Claim(p.id, p.by); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.calls.Settle(p.id, p.by, okResult(`[{"type":"text","text":"done"}]`)); err != nil {
			t.Fatalf("settle %s: %v", p.id, err)
		}
		if o := wait(t, p.out); !o.res.OK {
			t.Fatalf("outcome = %+v", o)
		}
	}
}

func TestClaimOfSettledOrUnknownRequestIs404(t *testing.T) {
	f := newCallFixture(t)
	out, req := f.start(t, context.Background(), "alfred", "browser_click")
	if _, err := f.calls.Claim(req.ID, f.a); err != nil {
		t.Fatal(err)
	}
	if err := f.calls.Settle(req.ID, f.a, okResult(`[]`)); err != nil {
		t.Fatal(err)
	}
	wait(t, out)
	if _, err := f.calls.Claim(req.ID, f.a); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("claim of settled err = %v", err)
	}
	if _, err := f.calls.Claim("ctr_nope", f.a); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("claim of unknown err = %v", err)
	}
	if err := f.calls.Settle("ctr_nope", f.a, okResult(`[]`)); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("settle of unknown err = %v", err)
	}
}

func TestSecondResult409(t *testing.T) {
	f := newCallFixture(t)
	out, req := f.start(t, context.Background(), "alfred", "browser_click")
	if err := f.calls.Settle(req.ID, f.a, okResult(`[]`)); !errors.Is(err, ErrNotClaimant) {
		t.Fatalf("unclaimed settle err = %v, want ErrNotClaimant", err)
	}
	if _, err := f.calls.Claim(req.ID, f.a); err != nil {
		t.Fatal(err)
	}
	if err := f.calls.Settle(req.ID, f.b, okResult(`[]`)); !errors.Is(err, ErrNotClaimant) {
		t.Fatalf("wrong client err = %v, want ErrNotClaimant", err)
	}
	if err := f.calls.Settle(req.ID, f.a, okResult(`[]`)); err != nil {
		t.Fatal(err)
	}
	wait(t, out)
	if err := f.calls.Settle(req.ID, f.a, okResult(`[]`)); !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("second result err = %v, want ErrAlreadySettled", err)
	}
}

func TestSettleRejectsMalformedResults(t *testing.T) {
	f := newCallFixture(t)
	_, req := f.start(t, context.Background(), "alfred", "browser_click")
	if _, err := f.calls.Claim(req.ID, f.a); err != nil {
		t.Fatal(err)
	}
	for name, res := range map[string]Result{
		"content not an array": {OK: true, Content: json.RawMessage(`{"type":"text"}`)},
		"failure without code": {OK: false, Error: &ToolError{Message: "x"}},
		"failure without body": {OK: false},
	} {
		if err := f.calls.Settle(req.ID, f.a, res); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("%s: err = %v, want ErrInvalidResult", name, err)
		}
	}
}

func TestOwnerDisconnectReleasesAndUnclaims(t *testing.T) {
	f := newCallFixture(t)
	out, req := f.start(t, context.Background(), "alfred", "browser_click")
	if _, err := f.calls.Claim(req.ID, f.b); err != nil {
		t.Fatal(err)
	}

	f.calls.ReleaseClient(f.b)

	// The in-flight request is announced again, unowned, and claimable by the other provider.
	again := f.pub.next(t, observe.EventClientToolRequest).Payload.(*observe.ClientToolRequestPayload)
	if again.Request.ID != req.ID || again.Request.Owner != "" {
		t.Fatalf("re-announced request = %+v", again.Request)
	}
	claimed, err := f.calls.Claim(req.ID, f.a)
	if err != nil || claimed.Owner != string(f.a) {
		t.Fatalf("takeover claim: %+v %v", claimed, err)
	}
	if err := f.calls.Settle(req.ID, f.b, okResult(`[]`)); !errors.Is(err, ErrNotClaimant) {
		t.Fatalf("old owner settles: %v", err)
	}
	if err := f.calls.Settle(req.ID, f.a, okResult(`[]`)); err != nil {
		t.Fatal(err)
	}
	if o := wait(t, out); !o.res.OK {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestOwnerDisconnectReannouncesUnclaimedRequestsItOwned(t *testing.T) {
	f := newCallFixture(t)
	// A becomes the owner of alfred's browser group by serving one call.
	first, firstReq := f.start(t, context.Background(), "alfred", "browser_click")
	if _, err := f.calls.Claim(firstReq.ID, f.a); err != nil {
		t.Fatal(err)
	}
	if err := f.calls.Settle(firstReq.ID, f.a, okResult(`[]`)); err != nil {
		t.Fatal(err)
	}
	wait(t, first)

	// The next call is published owned by A; A leaves before claiming it.
	second, req := f.start(t, context.Background(), "alfred", "browser_click")
	if req.Owner != string(f.a) {
		t.Fatalf("request owner = %q, want %q", req.Owner, f.a)
	}
	f.reg.Disconnect(f.a)
	f.calls.ReleaseClient(f.a)

	// Apps skip requests owned by someone else, so B only sees it if it is announced again, unowned.
	again := f.pub.next(t, observe.EventClientToolRequest).Payload.(*observe.ClientToolRequestPayload)
	if again.Request.ID != req.ID || again.Request.Owner != "" {
		t.Fatalf("re-announced request = %+v", again.Request)
	}
	if pending := f.calls.Pending(); len(pending) != 1 || pending[0].Owner != "" {
		t.Fatalf("pending = %+v, want the request unowned", pending)
	}
	if _, err := f.calls.Claim(req.ID, f.b); err != nil {
		t.Fatalf("B claims: %v", err)
	}
	if err := f.calls.Settle(req.ID, f.b, okResult(`[]`)); err != nil {
		t.Fatal(err)
	}
	if o := wait(t, second); !o.res.OK {
		t.Fatalf("outcome = %+v", o)
	}
}

type discardPublisher struct{}

func (discardPublisher) Publish(observe.Event) {}

// Call publishes its request while an owner leaving rewrites that request's
// owner. There is no seam between parking and publishing to interleave them
// deterministically, so this runs them against each other for the race
// detector (-race) to catch an unlocked read.
func TestReleaseWhileCallsPublishDoesNotRace(t *testing.T) {
	f := newCallFixtureWith(t, func(o *CallsOptions) { o.Publish = discardPublisher{} })
	ctx, cancel := context.WithCancel(context.Background())
	var calls sync.WaitGroup
	call := func() {
		calls.Add(1)
		go func() {
			defer calls.Done()
			_, _ = f.calls.Call(ctx, "alfred", "browser_click", json.RawMessage(`{}`))
		}()
	}
	call()
	for i := 0; i < 300; i++ {
		// Make A the owner (claiming any parked request does), then let a new
		// call park owned by A while A leaves.
		for _, p := range f.calls.Pending() {
			if _, err := f.calls.Claim(p.ID, f.a); err == nil {
				break
			}
		}
		call()
		f.calls.ReleaseClient(f.a)
	}
	cancel()
	calls.Wait()
}

func TestPendingListsUnsettled(t *testing.T) {
	f := newCallFixture(t)
	_, r1 := f.start(t, context.Background(), "alfred", "browser_click")
	_, r2 := f.start(t, context.Background(), "alfred", "browser_click")
	if _, err := f.calls.Claim(r2.ID, f.a); err != nil {
		t.Fatal(err)
	}
	pending := f.calls.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending = %+v", pending)
	}
	byID := map[string]PendingRequest{}
	for _, p := range pending {
		byID[p.ID] = p
	}
	if byID[r1.ID].ClaimedBy != "" || byID[r2.ID].ClaimedBy != string(f.a) || byID[r2.ID].Owner != string(f.a) {
		t.Fatalf("pending = %+v", pending)
	}
	if pending[0].ID != r1.ID {
		t.Fatalf("pending not in creation order: %+v", pending)
	}
}

func TestCancelAgentCancelsItsParkedCalls(t *testing.T) {
	f := newCallFixture(t)
	outAlfred, reqAlfred := f.start(t, context.Background(), "alfred", "browser_handoff")
	outBob, _ := f.start(t, context.Background(), "bob", "browser_handoff")

	f.calls.CancelAgent("alfred")

	o := wait(t, outAlfred)
	if o.res.OK || o.res.Error == nil || o.res.Error.Code != "cancelled" {
		t.Fatalf("outcome = %+v", o)
	}
	c := f.pub.next(t, observe.EventClientToolCancelled).Payload.(*observe.ClientToolCancelledPayload)
	if c.ID != reqAlfred.ID || c.Reason != observe.CancelAgentGone {
		t.Fatalf("cancelled = %+v", c)
	}
	select {
	case o := <-outBob:
		t.Fatalf("bob's call ended: %+v", o)
	default:
	}
	if got := f.calls.Pending(); len(got) != 1 || got[0].Agent != "bob" {
		t.Fatalf("pending = %+v", got)
	}
}

func TestFailGroupFailsParkedCallsWithNoClient(t *testing.T) {
	f := newCallFixture(t)
	out, _ := f.start(t, context.Background(), "alfred", "browser_handoff")
	f.calls.FailGroup("browser")
	o := wait(t, out)
	if o.res.OK || o.res.Error == nil || o.res.Error.Code != "no_client" {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestImageContentPassThrough(t *testing.T) {
	f := newCallFixture(t)
	out, req := f.start(t, context.Background(), "alfred", "browser_click")
	if _, err := f.calls.Claim(req.ID, f.a); err != nil {
		t.Fatal(err)
	}
	// Whitespace and key order are the app's; the relay must not normalise them.
	raw := `[ {"type":"text","text":"clicked"},  {"mimeType":"image/png","type":"image","data":"iVBORw0KGgo="} ]`
	if err := f.calls.Settle(req.ID, f.a, okResult(raw)); err != nil {
		t.Fatal(err)
	}
	o := wait(t, out)
	if !o.res.OK || !bytes.Equal(o.res.Content, []byte(raw)) {
		t.Fatalf("content = %s", o.res.Content)
	}
}

func TestErrorResultPassesThrough(t *testing.T) {
	f := newCallFixture(t)
	out, req := f.start(t, context.Background(), "alfred", "browser_click")
	if _, err := f.calls.Claim(req.ID, f.a); err != nil {
		t.Fatal(err)
	}
	failure := Result{OK: false, Error: &ToolError{Code: "user_has_control", Message: "the user is controlling the browser"}}
	if err := f.calls.Settle(req.ID, f.a, failure); err != nil {
		t.Fatal(err)
	}
	o := wait(t, out)
	if o.res.OK || o.res.Error == nil || *o.res.Error != *failure.Error {
		t.Fatalf("outcome = %+v", o)
	}
}
