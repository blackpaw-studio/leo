package machine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeProbe struct {
	mu       sync.Mutex
	ticks    CPUTicks
	ticksErr error
	mem      Memory
	memErr   error
	disk     Disk
	diskErr  error
	calls    int
	// gate, when set, blocks CPUTicks until closed; entered is signalled first.
	gate    chan struct{}
	entered chan struct{}
}

func (p *fakeProbe) Cores() int { return 8 }

func (p *fakeProbe) CPUTicks() (CPUTicks, error) {
	p.mu.Lock()
	p.calls++
	gate, entered := p.gate, p.entered
	t, err := p.ticks, p.ticksErr
	p.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	return t, err
}

func (p *fakeProbe) Memory() (Memory, error) { return p.mem, p.memErr }
func (p *fakeProbe) Disk() (Disk, error)     { return p.disk, p.diskErr }

func (p *fakeProbe) set(busy, idle uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ticks = CPUTicks{Busy: busy, Idle: idle}
}

func (p *fakeProbe) cpuCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func newTestSampler(p *fakeProbe) (*Sampler, *fakeClock, *logSink) {
	clk := &fakeClock{t: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	logs := &logSink{}
	p.mem = Memory{UsedBytes: 4, TotalBytes: 16}
	p.disk = Disk{Path: "/state", UsedBytes: 10, TotalBytes: 100}
	return NewSampler(p, WithClock(clk.Now), WithLogf(logs.logf)), clk, logs
}

func TestFirstSampleHasNullPercentAndFullSections(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)

	got := s.Sample()

	if got.CPU.Percent != nil {
		t.Fatalf("percent = %v, want nil", *got.CPU.Percent)
	}
	if got.CPU.Cores != 8 {
		t.Fatalf("cores = %d, want 8", got.CPU.Cores)
	}
	if got.Memory == nil || *got.Memory != p.mem {
		t.Fatalf("memory = %+v, want %+v", got.Memory, p.mem)
	}
	if got.Disk == nil || *got.Disk != p.disk {
		t.Fatalf("disk = %+v, want %+v", got.Disk, p.disk)
	}
	if !got.SampledAt.Equal(clk.Now()) {
		t.Fatalf("sampled_at = %v, want %v", got.SampledAt, clk.Now())
	}
}

func TestPercentIsBusyShareOfTickDelta(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(2 * time.Second)
	p.set(150, 250) // Δbusy 50 of Δtotal 200
	got := s.Sample()

	if got.CPU.Percent == nil || *got.CPU.Percent != 25 {
		t.Fatalf("percent = %v, want 25", got.CPU.Percent)
	}
}

func TestSampleWithinOneSecondReturnsCache(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)
	first := s.Sample()

	clk.Advance(500 * time.Millisecond)
	second := s.Sample()

	if p.cpuCalls() != 1 {
		t.Fatalf("probe calls = %d, want 1", p.cpuCalls())
	}
	if !second.SampledAt.Equal(first.SampledAt) {
		t.Fatalf("sampled_at changed within cache window: %v vs %v", first.SampledAt, second.SampledAt)
	}
}

func TestSampleAtExactlyOneSecondResamples(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(time.Second)
	s.Sample()

	if p.cpuCalls() != 2 {
		t.Fatalf("probe calls = %d, want 2", p.cpuCalls())
	}
}

func TestStaleBaselineYieldsNullThenRecovers(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(61 * time.Second)
	p.set(200, 200)
	if got := s.Sample(); got.CPU.Percent != nil {
		t.Fatalf("stale baseline percent = %v, want nil", *got.CPU.Percent)
	}

	clk.Advance(2 * time.Second)
	p.set(250, 250) // against the reset baseline: 50 of 100
	got := s.Sample()
	if got.CPU.Percent == nil || *got.CPU.Percent != 50 {
		t.Fatalf("percent after reset = %v, want 50", got.CPU.Percent)
	}
}

func TestBaselineExactlySixtySecondsStillCounts(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(60 * time.Second)
	p.set(110, 190)
	got := s.Sample()

	if got.CPU.Percent == nil || *got.CPU.Percent != 10 {
		t.Fatalf("percent = %v, want 10", got.CPU.Percent)
	}
}

func TestCounterGoingDownYieldsNullAndResetsBaseline(t *testing.T) {
	p := &fakeProbe{}
	p.set(1000, 1000)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(2 * time.Second)
	p.set(10, 1100) // busy counter wrapped
	if got := s.Sample(); got.CPU.Percent != nil {
		t.Fatalf("percent after wrap = %v, want nil", *got.CPU.Percent)
	}

	clk.Advance(2 * time.Second)
	p.set(60, 1150) // 50 of 100 against the new baseline
	got := s.Sample()
	if got.CPU.Percent == nil || *got.CPU.Percent != 50 {
		t.Fatalf("percent = %v, want 50", got.CPU.Percent)
	}
}

func TestZeroTickDeltaYieldsNull(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(2 * time.Second)
	got := s.Sample()

	if got.CPU.Percent != nil {
		t.Fatalf("percent = %v, want nil", *got.CPU.Percent)
	}
}

func TestPercentIsClampedToRange(t *testing.T) {
	p := &fakeProbe{}
	p.set(0, 0)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(2 * time.Second)
	p.set(100, 0)
	got := s.Sample()

	if got.CPU.Percent == nil || *got.CPU.Percent != 100 {
		t.Fatalf("percent = %v, want 100", got.CPU.Percent)
	}
}

func TestCPUErrorKeepsCoresAndBaseline(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, _ := newTestSampler(p)
	s.Sample()

	clk.Advance(2 * time.Second)
	p.mu.Lock()
	p.ticksErr = errors.New("boom")
	p.mu.Unlock()
	failed := s.Sample()
	if failed.CPU.Percent != nil || failed.CPU.Cores != 8 {
		t.Fatalf("failed cpu = %+v, want nil percent and 8 cores", failed.CPU)
	}

	clk.Advance(2 * time.Second)
	p.mu.Lock()
	p.ticksErr = nil
	p.mu.Unlock()
	p.set(150, 250) // original baseline kept: 50 of 200
	got := s.Sample()
	if got.CPU.Percent == nil || *got.CPU.Percent != 25 {
		t.Fatalf("percent = %v, want 25", got.CPU.Percent)
	}
}

func TestSectionErrorsNullOnlyThatSection(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, _, _ := newTestSampler(p)
	p.memErr = errors.New("no mem")

	got := s.Sample()
	if got.Memory != nil || got.Disk == nil {
		t.Fatalf("memory=%v disk=%v, want nil memory and set disk", got.Memory, got.Disk)
	}

	p2 := &fakeProbe{}
	p2.set(100, 100)
	s2, _, _ := newTestSampler(p2)
	p2.diskErr = errors.New("no disk")
	got = s2.Sample()
	if got.Disk != nil || got.Memory == nil {
		t.Fatalf("memory=%v disk=%v, want set memory and nil disk", got.Memory, got.Disk)
	}
}

func TestProbePanicIsTreatedAsSectionError(t *testing.T) {
	p := &panicProbe{}
	s := NewSampler(p, WithClock(func() time.Time { return time.Unix(0, 0) }), WithLogf((&logSink{}).logf))

	got := s.Sample()

	if got.Memory != nil || got.CPU.Percent != nil || got.CPU.Cores != 2 {
		t.Fatalf("got %+v, want nil memory/percent and 2 cores", got)
	}
}

type panicProbe struct{}

func (panicProbe) Cores() int                  { return 2 }
func (panicProbe) CPUTicks() (CPUTicks, error) { return CPUTicks{}, nil }
func (panicProbe) Memory() (Memory, error)     { panic("ffi exploded") }
func (panicProbe) Disk() (Disk, error)         { return Disk{Path: "/"}, nil }

func TestConcurrentCallersShareOneInFlightSample(t *testing.T) {
	p := &fakeProbe{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	p.set(100, 100)
	s, _, _ := newTestSampler(p)
	waiting := make(chan struct{}, 1)
	s.onWait = func() { waiting <- struct{}{} }

	results := make(chan Stats, 2)
	go func() { results <- s.Sample() }()
	<-p.entered // first caller is inside the probe
	go func() { results <- s.Sample() }()
	<-waiting // second caller is parked on the in-flight call
	close(p.gate)

	a, b := <-results, <-results
	if p.cpuCalls() != 1 {
		t.Fatalf("probe calls = %d, want 1", p.cpuCalls())
	}
	if !a.SampledAt.Equal(b.SampledAt) {
		t.Fatalf("callers got different samples: %v vs %v", a.SampledAt, b.SampledAt)
	}
}

func TestRepeatedErrorLogsOnceAndRecoveryLogsOnce(t *testing.T) {
	p := &fakeProbe{}
	p.set(100, 100)
	s, clk, logs := newTestSampler(p)
	p.memErr = errors.New("mem broke")

	for range 3 {
		s.Sample()
		clk.Advance(2 * time.Second)
	}
	failures := countContaining(logs, "mem broke")
	if failures != 1 {
		t.Fatalf("failure log lines = %d, want 1 (%v)", failures, logs.lines)
	}

	p.memErr = nil
	for range 2 {
		s.Sample()
		clk.Advance(2 * time.Second)
	}
	if n := countContaining(logs, "recovered"); n != 1 {
		t.Fatalf("recovery log lines = %d, want 1 (%v)", n, logs.lines)
	}
}

func countContaining(l *logSink, sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}
