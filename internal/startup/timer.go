// Package startup times the daemon's boot sequence so a stall can be
// located from service.log alone.
package startup

import "time"

// Timer logs how long each startup step took. Every method is safe on a nil
// receiver, so callers that have no timer behave exactly as before.
type Timer struct {
	now   func() time.Time
	logf  func(format string, args ...any)
	begin time.Time
}

// New returns a Timer reading time from now and writing lines through logf.
func New(now func() time.Time, logf func(string, ...any)) *Timer {
	return &Timer{now: now, logf: logf, begin: now()}
}

// Step starts timing name; call the returned func when the step finishes.
func (t *Timer) Step(name string) (done func()) {
	if t == nil {
		return func() {}
	}
	started := t.now()
	return func() { t.logf("startup: %s took %s", name, t.now().Sub(started)) }
}

// Total logs the time since New.
func (t *Timer) Total() {
	if t == nil {
		return
	}
	t.logf("startup: total took %s", t.now().Sub(t.begin))
}

// Logf writes a detail line through the same logger.
func (t *Timer) Logf(format string, args ...any) {
	if t == nil {
		return
	}
	t.logf(format, args...)
}
