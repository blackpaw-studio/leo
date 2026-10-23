package consult

import "time"

// defaultSlowRestartRecord is how long one record's restart reconciliation may
// take before MarkInterrupted warns about it.
const defaultSlowRestartRecord = 2 * time.Second

// SetStartupLog sets where startup diagnostics (slow restart records) are
// written. Nil, the default, discards them.
func (d *Dispatcher) SetStartupLog(logf func(format string, args ...any)) {
	d.startupLog = logf
}

// recordTimings tracks the slowest operation of one record's reconciliation.
type recordTimings struct {
	now     func() time.Time
	begin   time.Time
	slowest string
	longest time.Duration
}

func (d *Dispatcher) newRecordTimings() *recordTimings {
	return &recordTimings{now: d.now, begin: d.now()}
}

// start begins timing op; call the returned func when it finishes.
func (t *recordTimings) start(op string) (stop func()) {
	started := t.now()
	return func() {
		if took := t.now().Sub(started); took > t.longest {
			t.slowest, t.longest = op, took
		}
	}
}

// warnSlowRecord logs a record whose reconciliation exceeded the threshold,
// as soon as that record finishes, so a long stall still shows progress.
func (d *Dispatcher) warnSlowRecord(id string, t *recordTimings) {
	threshold := d.slowRestartRecord
	if threshold == 0 {
		threshold = defaultSlowRestartRecord
	}
	took := t.now().Sub(t.begin)
	if d.startupLog == nil || took <= threshold {
		return
	}
	d.startupLog("startup: MarkInterrupted: slow record %s took %s (slowest: %s %s)", id, took, t.slowest, t.longest)
}
