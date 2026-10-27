package follow

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// puller is what Loop needs from Puller.
type puller interface {
	Pull(ctx context.Context) (Outcome, error)
}

// Loop keeps a follower's cache fresh: it pulls at start and every
// follow.interval, on SyncNow, and on Kick when follow was just turned on
// or follow.host changed. A pull that changes the lead's config calls
// Reload once. Pulls are single-flight: a SyncNow during a running pull
// shares its result.
type Loop struct {
	Puller     puller
	LoadConfig func() (*config.Config, error)
	// Reload applies a changed pull in-process (the daemon's ReloadConfig).
	Reload func() error
	Now    func() time.Time
	// After is time.After in production; tests drive it.
	After func(time.Duration) <-chan time.Time
	// Logf reports background pull failures; log.Printf when nil.
	Logf func(format string, args ...any)

	kickOnce sync.Once
	kick     chan struct{}

	mu     sync.Mutex
	flight *flight
}

type flight struct {
	done    chan struct{}
	out     Outcome
	err     error
	waiters int
}

// NewLoop wires a Loop with the real ssh runner and clock.
func NewLoop(loadConfig func() (*config.Config, error), reload func() error) *Loop {
	return &Loop{
		Puller:     &Puller{LoadConfig: loadConfig, Runner: ExecRunner{}, Now: time.Now},
		LoadConfig: loadConfig,
		Reload:     reload,
		Now:        time.Now,
		After:      time.After,
	}
}

func (l *Loop) kicks() chan struct{} {
	l.kickOnce.Do(func() { l.kick = make(chan struct{}, 1) })
	return l.kick
}

// Kick asks the loop to re-read the follow config (after a config reload).
// It never blocks.
func (l *Loop) Kick() {
	select {
	case l.kicks() <- struct{}{}:
	default:
	}
}

// Run drives the schedule until ctx ends.
func (l *Loop) Run(ctx context.Context) {
	host, interval := l.settings("", 0)
	var lastPull time.Time
	if host != "" {
		lastPull = l.pullInBackground(ctx)
	}
	for {
		var tick <-chan time.Time
		if host != "" {
			tick = l.After(max(0, lastPull.Add(interval).Sub(l.Now())))
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
			lastPull = l.pullInBackground(ctx)
		case <-l.kicks():
			next, nextInterval := l.settings(host, interval)
			pull := next != "" && next != host
			host, interval = next, nextInterval
			if pull {
				lastPull = l.pullInBackground(ctx)
			}
		}
	}
}

// settings returns the follow host ("" when not following) and interval,
// keeping the previous ones when the config cannot be loaded.
func (l *Loop) settings(prevHost string, prevInterval time.Duration) (string, time.Duration) {
	cfg, err := l.LoadConfig()
	if err != nil {
		l.logf("follow: loading config: %v", err)
		return prevHost, prevInterval
	}
	if !cfg.IsFollower() {
		return "", 0
	}
	return cfg.Follow.Host, cfg.FollowInterval()
}

func (l *Loop) pullInBackground(ctx context.Context) time.Time {
	if _, err := l.SyncNow(ctx); err != nil {
		l.logf("follow: %v", err)
	}
	return l.Now()
}

// SyncNow pulls now, or joins the pull already running, and reloads the
// config when the pull changed it. ctx only bounds the wait: the pull itself
// runs to completion (it has its own timeout) for every caller sharing it.
func (l *Loop) SyncNow(ctx context.Context) (Outcome, error) {
	cfg, err := l.LoadConfig()
	if err != nil {
		return Outcome{}, fmt.Errorf("loading config: %w", err)
	}
	if !cfg.IsFollower() {
		return Outcome{}, ErrNotFollowing
	}
	l.mu.Lock()
	if f := l.flight; f != nil {
		f.waiters++
		l.mu.Unlock()
		select {
		case <-f.done:
			return f.out, f.err
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{}), waiters: 1}
	l.flight = f
	l.mu.Unlock()

	f.out, f.err = l.Puller.Pull(context.WithoutCancel(ctx))
	if f.err == nil && f.out.Changed && l.Reload != nil {
		if err := l.Reload(); err != nil {
			f.err = fmt.Errorf("pulled the lead config, but reloading it failed: %w", err)
		}
	}
	l.mu.Lock()
	l.flight = nil
	l.mu.Unlock()
	close(f.done)
	return f.out, f.err
}

func (l *Loop) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}
