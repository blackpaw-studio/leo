package cron

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"github.com/blackpaw-studio/leo/internal/config"
)

// EntryInfo describes a scheduled task for listing purposes.
type EntryInfo struct {
	Name     string    `json:"name"`
	Schedule string    `json:"schedule"`
	Next     time.Time `json:"next"`
}

// Scheduler wraps robfig/cron/v3 and tracks task-to-entry mappings.
type Scheduler struct {
	mu      sync.Mutex
	cron    *robfigcron.Cron
	entries map[string]robfigcron.EntryID
	tasks   map[string]config.TaskConfig
	leoPath string
	cfgPath string
	runFn   func(leoPath, cfgPath, taskName string)
	// spawn starts a released or run-now task off the caller's goroutine,
	// as robfig does for scheduled firings; tests make it synchronous.
	spawn func(func())

	// holdMu guards the drain hold: while held, firings are recorded in
	// heldTasks (once per task, first-fire order) instead of run. It is
	// separate from mu so a firing never waits on Install.
	holdMu    sync.Mutex
	held      bool
	heldTasks []string
}

// New creates a new Scheduler. Call Start() to begin firing.
func New(leoPath, cfgPath string) *Scheduler {
	s := &Scheduler{
		cron:    robfigcron.New(),
		entries: make(map[string]robfigcron.EntryID),
		tasks:   make(map[string]config.TaskConfig),
		leoPath: leoPath,
		cfgPath: cfgPath,
	}
	s.runFn = defaultRunTask
	s.spawn = func(f func()) { go f() }
	return s
}

// Start begins the cron scheduler goroutine.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop gracefully waits for running jobs then stops the scheduler.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// Install loads all enabled tasks from config and schedules them.
// It removes any previously scheduled entries first (full sync).
// If individual tasks fail to schedule, the remaining tasks are still
// added and a combined error is returned.
func (s *Scheduler) Install(cfg *config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing entries
	for name, id := range s.entries {
		s.cron.Remove(id)
		delete(s.entries, name)
		delete(s.tasks, name)
	}

	// Add enabled tasks, collecting errors instead of bailing on first failure
	var errs []string
	for name, task := range cfg.Tasks {
		if !task.Enabled {
			continue
		}
		if err := s.addLocked(name, task); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to schedule: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Remove unschedules all entries.
func (s *Scheduler) Remove() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, id := range s.entries {
		s.cron.Remove(id)
		delete(s.entries, name)
		delete(s.tasks, name)
	}
}

// List returns info about all scheduled entries, sorted by name.
func (s *Scheduler) List() []EntryInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]EntryInfo, 0, len(s.entries))
	for name, id := range s.entries {
		entry := s.cron.Entry(id)
		task := s.tasks[name]
		infos = append(infos, EntryInfo{
			Name:     name,
			Schedule: task.Schedule,
			Next:     entry.Next,
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// addLocked adds a single task. Caller must hold s.mu.
func (s *Scheduler) addLocked(name string, task config.TaskConfig) error {
	spec := task.Schedule
	if task.Timezone != "" {
		spec = fmt.Sprintf("CRON_TZ=%s %s", task.Timezone, spec)
	}

	taskName := name
	leoPath := s.leoPath
	cfgPath := s.cfgPath
	runFn := s.runFn

	id, err := s.cron.AddFunc(spec, func() {
		if s.deferFiring(taskName) {
			return
		}
		runFn(leoPath, cfgPath, taskName)
	})
	if err != nil {
		return fmt.Errorf("parsing schedule %q: %w", spec, err)
	}

	s.entries[name] = id
	s.tasks[name] = task
	return nil
}

// Hold defers cron firings until Release. The hold is independent of the
// schedule, so it survives Install (config reload).
func (s *Scheduler) Hold() {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	s.held = true
}

// Release ends a hold and runs each deferred task that is still scheduled,
// once.
func (s *Scheduler) Release() {
	s.holdMu.Lock()
	deferred := s.heldTasks
	s.held, s.heldTasks = false, nil
	s.holdMu.Unlock()
	s.RunNow(deferred)
}

// Held returns the tasks whose firings the current hold deferred, once
// each, in first-fire order.
func (s *Scheduler) Held() []string {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	return append([]string(nil), s.heldTasks...)
}

// RunNow runs each named task that is still scheduled; unknown names are
// skipped. A new daemon uses it for firings its predecessor deferred.
func (s *Scheduler) RunNow(names []string) {
	s.mu.Lock()
	runFn, leoPath, cfgPath := s.runFn, s.leoPath, s.cfgPath
	var due []string
	for _, name := range names {
		if _, ok := s.tasks[name]; ok {
			due = append(due, name)
		}
	}
	s.mu.Unlock()
	for _, name := range due {
		s.spawn(func() { runFn(leoPath, cfgPath, name) })
	}
}

// deferFiring records taskName's firing and reports true while held.
func (s *Scheduler) deferFiring(taskName string) bool {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	if !s.held {
		return false
	}
	for _, name := range s.heldTasks {
		if name == taskName {
			return true
		}
	}
	s.heldTasks = append(s.heldTasks, taskName)
	return true
}

func defaultRunTask(leoPath, cfgPath, taskName string) {
	// Use a generous outer timeout; the inner `leo run` command enforces
	// the per-task timeout from config (default 30m). This outer limit
	// is a safety net for the subprocess itself.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, leoPath, "run", taskName, "--config", cfgPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cron: task %q failed: %v\nOutput: %s\n", taskName, err, output)
	}
}
