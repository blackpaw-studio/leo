package consult

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
)

// transcriptLivePoll is how often a running interactive claude dispatch's
// transcript is re-read for its live token counts.
const transcriptLivePoll = 3 * time.Second

// liveUsageConfig is how live usage finds and reads claude transcripts.
type liveUsageConfig struct {
	poll time.Duration
	read TranscriptReader
	// path locates a session's transcript when no hook reported its path.
	path func(cwd, sessionID string) (string, error)
}

func defaultLiveUsageConfig() liveUsageConfig {
	return liveUsageConfig{poll: transcriptLivePoll, read: readTranscriptFrom, path: session.JSONLPath}
}

// liveUsage is an interactive claude run's transcript-derived token counts.
// Turn reports (ApplyBridgeTokens) stay authoritative: live counts show only
// what the transcript reported since the last of them, added to theirs.
type liveUsage struct {
	// mu guards tally. It is held across transcript reads, never while
	// taking d.mu; d.mu may be held while taking it.
	mu    sync.Mutex
	tally transcriptTally
	// The rest is guarded by d.mu. transcriptPath and sessionID are the
	// latest a hook reported; baseIn/baseOut are the tally's totals when
	// the last turn report was applied. boundaryIn/boundaryOut, while
	// hasBoundary, are the totals a completed turn was caught up to, taken
	// before the run went idle and could take a follow-up.
	transcriptPath, sessionID string
	baseIn, baseOut           int64
	boundaryIn, boundaryOut   int64
	hasBoundary               bool
}

// noteLiveTranscriptLocked records where a hook payload says the run's
// transcript is.
func noteLiveTranscriptLocked(s *runState, p map[string]any) {
	if path := str(p, "transcript_path"); path != "" {
		s.live.transcriptPath = path
	}
	if sid := str(p, "session_id"); sid != "" {
		s.live.sessionID = sid
	}
}

// watchLiveUsage polls the run's transcript until the run ends or the
// daemon stops.
func (d *Dispatcher) watchLiveUsage(s *runState) {
	ticker := time.NewTicker(d.liveUsage.poll)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-d.daemonCtx.Done():
			return
		case <-ticker.C:
			d.refreshLiveUsage(s)
		}
	}
}

// refreshLiveUsage reads what the transcript appended and shows it as the
// working turn's live usage.
func (d *Dispatcher) refreshLiveUsage(s *runState) {
	in, out, ok := d.advanceLiveTally(s)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyLiveUsageLocked(s, in, out)
}

// advanceLiveTally brings the tally up to date with the transcript and
// returns its totals; ok is false while the run has no transcript to read,
// and the totals are then those already read.
func (d *Dispatcher) advanceLiveTally(s *runState) (in, out int64, ok bool) {
	d.mu.Lock()
	path, ok := d.liveTranscriptPathLocked(s)
	d.mu.Unlock()
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if ok {
		if err := s.live.tally.advance(d.liveUsage.read, path); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: reading transcript for live usage: %v\n", s.record.ID, err)
		}
	}
	in, out = s.live.tally.totals()
	return in, out, ok
}

func (d *Dispatcher) liveTranscriptPathLocked(s *runState) (string, bool) {
	if s.record.Status.Terminal() {
		return "", false
	}
	if s.live.transcriptPath != "" {
		return s.live.transcriptPath, true
	}
	if s.live.sessionID == "" || d.liveUsage.path == nil {
		return "", false
	}
	cwd := s.record.Cwd
	if s.record.Worktree != "" {
		cwd = s.record.Worktree
	}
	path, err := d.liveUsage.path(cwd, s.live.sessionID)
	return path, err == nil
}

func (d *Dispatcher) applyLiveUsageLocked(s *runState, in, out int64) {
	if s.record.Status.Terminal() || !hasWorkingTurnLocked(s) {
		return
	}
	dIn, dOut := max(in-s.live.baseIn, 0), max(out-s.live.baseOut, 0)
	if dIn == 0 && dOut == 0 {
		return
	}
	reported := s.bridgedUsage
	usage := &harness.Usage{
		InputTokens:  harness.Int64(reported.input + dIn),
		OutputTokens: harness.Int64(reported.output + dOut),
		CostUSD:      clonePtr(reported.cost),
		Incomplete:   true,
	}
	if len(s.record.UsageInvocations) == 0 {
		d.beginUsageInvocationLocked(s)
	}
	if sameLiveUsage(s.record.UsageInvocations[len(s.record.UsageInvocations)-1], usage) {
		return
	}
	d.applyUsageLocked(s, usage, false)
}

func sameLiveUsage(cur InvocationUsage, u *harness.Usage) bool {
	return cur.Incomplete && cur.InputTokens != nil && cur.OutputTokens != nil &&
		*cur.InputTokens == *u.InputTokens && *cur.OutputTokens == *u.OutputTokens
}

// CatchUpLiveUsage reads a run's transcript to its end as a turn completes,
// and holds the totals as the boundary its turn report will rebase on. The
// bridge subscriber calls it before reporting the turn's Stop, so nothing a
// follow-up adds can fall inside the boundary. A failed read holds none:
// the base stays where it was and the watcher recovers on its next poll.
func (d *Dispatcher) CatchUpLiveUsage(id string) {
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		return
	}
	d.catchUpLive(s)
}

func (d *Dispatcher) catchUpLive(s *runState) {
	d.mu.Lock()
	path, ok := d.liveTranscriptPathLocked(s)
	d.mu.Unlock()
	s.live.mu.Lock()
	var err error
	if ok {
		err = s.live.tally.drain(d.liveUsage.read, path)
	}
	in, out := s.live.tally.totals()
	s.live.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		s.live.hasBoundary = false
		fmt.Fprintf(os.Stderr, "dispatch %s: catching up transcript for live usage: %v\n", s.record.ID, err)
		return
	}
	s.live.boundaryIn, s.live.boundaryOut, s.live.hasBoundary = in, out, true
}

// liveBoundaryHeld reports whether a catch-up already holds a boundary for
// the turn report about to be applied.
func (d *Dispatcher) liveBoundaryHeld(s *runState) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return s.live.hasBoundary
}

// rebaseLiveUsageLocked makes the held boundary the point live counts start
// from, once a turn report has accounted for it. Without one (the catch-up
// failed) the base stays put.
func rebaseLiveUsageLocked(s *runState) {
	if !s.live.hasBoundary {
		return
	}
	s.live.baseIn, s.live.baseOut, s.live.hasBoundary = s.live.boundaryIn, s.live.boundaryOut, false
}
