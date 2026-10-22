package machine

import (
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	// cacheTTL is how long a sample is reused; concurrent pollers share it.
	cacheTTL = time.Second
	// baselineMaxAge is the oldest CPU baseline that still yields a percent.
	baselineMaxAge = 60 * time.Second
)

// Sampler turns a Probe into Stats. It samples on demand, caches the result
// for cacheTTL and lets concurrent callers share one in-flight sample.
type Sampler struct {
	probe Probe
	now   func() time.Time
	logf  func(format string, args ...any)

	// onWait is called when a caller parks on an in-flight sample; tests use
	// it to detect that without sleeping.
	onWait func()

	mu          sync.Mutex
	cache       Stats
	cacheAt     time.Time
	hasCache    bool
	inflight    *sampleCall
	baseline    CPUTicks
	baselineAt  time.Time
	hasBaseline bool
	lastErr     map[string]string
}

type sampleCall struct {
	done  chan struct{}
	stats Stats
}

// SamplerOption configures NewSampler.
type SamplerOption func(*Sampler)

// WithClock injects the time source.
func WithClock(now func() time.Time) SamplerOption {
	return func(s *Sampler) { s.now = now }
}

// WithLogf injects the logger for probe failures.
func WithLogf(logf func(format string, args ...any)) SamplerOption {
	return func(s *Sampler) { s.logf = logf }
}

// NewSampler returns a Sampler reading from p.
func NewSampler(p Probe, opts ...SamplerOption) *Sampler {
	s := &Sampler{
		probe:   p,
		now:     time.Now,
		logf:    log.Printf,
		onWait:  func() {},
		lastErr: map[string]string{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Sample returns the current machine stats, reusing a sample younger than
// cacheTTL and joining one already in flight.
func (s *Sampler) Sample() Stats {
	s.mu.Lock()
	now := s.now()
	if s.hasCache && now.Sub(s.cacheAt) < cacheTTL {
		cached := s.cache
		s.mu.Unlock()
		return cached
	}
	if c := s.inflight; c != nil {
		s.mu.Unlock()
		s.onWait()
		<-c.done
		return c.stats
	}
	c := &sampleCall{done: make(chan struct{})}
	s.inflight = c
	s.mu.Unlock()

	r := s.read()

	s.mu.Lock()
	stats := s.assemble(now, r)
	s.cache, s.cacheAt, s.hasCache = stats, now, true
	s.inflight = nil
	c.stats = stats
	s.mu.Unlock()
	close(c.done)
	return stats
}

// reading is one raw pass over the probes, taken outside the lock.
type reading struct {
	cores   int
	ticks   CPUTicks
	ticksEr error
	mem     Memory
	memErr  error
	disk    Disk
	diskErr error
}

func (s *Sampler) read() reading {
	r := reading{cores: s.probe.Cores()}
	r.ticks, r.ticksEr = guard(s.probe.CPUTicks)
	r.mem, r.memErr = guard(s.probe.Memory)
	r.disk, r.diskErr = guard(s.probe.Disk)
	return r
}

// guard converts a Go-side probe panic into an error so one bad section can't
// wedge the in-flight call. It cannot recover a fault inside libSystem (a
// SIGSEGV in the macOS FFI calls kills the process).
func guard[T any](f func() (T, error)) (v T, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("probe panicked: %v", p)
		}
	}()
	return f()
}

// assemble builds Stats from a reading. The caller holds s.mu.
func (s *Sampler) assemble(now time.Time, r reading) Stats {
	st := Stats{SampledAt: now, CPU: CPU{Cores: r.cores}}
	s.track("cpu", r.ticksEr)
	s.track("memory", r.memErr)
	s.track("disk", r.diskErr)
	if r.ticksEr == nil {
		st.CPU.Percent = s.cpuPercent(now, r.ticks)
	}
	if r.memErr == nil {
		m := r.mem
		st.Memory = &m
	}
	if r.diskErr == nil {
		d := r.disk
		st.Disk = &d
	}
	return st
}

// cpuPercent compares ticks with the baseline and makes ticks the new one.
// It returns nil when there is no usable baseline: none yet, older than
// baselineMaxAge, a counter that went down, or no ticks elapsed.
func (s *Sampler) cpuPercent(now time.Time, ticks CPUTicks) *float64 {
	prev, prevAt, ok := s.baseline, s.baselineAt, s.hasBaseline
	s.baseline, s.baselineAt, s.hasBaseline = ticks, now, true

	if !ok || now.Sub(prevAt) > baselineMaxAge {
		return nil
	}
	if ticks.Busy < prev.Busy || ticks.Idle < prev.Idle {
		return nil
	}
	busy := ticks.Busy - prev.Busy
	total := busy + (ticks.Idle - prev.Idle)
	if total == 0 {
		return nil
	}
	pct := min(100, max(0, 100*float64(busy)/float64(total)))
	return &pct
}

// track logs a section's failure once per distinct error text, and its
// recovery once, so a probe that keeps failing doesn't flood the log.
func (s *Sampler) track(section string, err error) {
	prev, failing := s.lastErr[section]
	switch {
	case err == nil && failing:
		delete(s.lastErr, section)
		s.logf("machine stats: %s probe recovered", section)
	case err != nil && (!failing || prev != err.Error()):
		s.lastErr[section] = err.Error()
		s.logf("machine stats: %s probe failed: %v", section, err)
	}
}
