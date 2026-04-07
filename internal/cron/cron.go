package cron

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
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
func (s *Scheduler) Install(cfg *config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing entries
	for name, id := range s.entries {
		s.cron.Remove(id)
		delete(s.entries, name)
		delete(s.tasks, name)
	}

	// Add enabled tasks
	for name, task := range cfg.Tasks {
		if !task.Enabled {
			continue
		}
		if err := s.addLocked(name, task); err != nil {
			return fmt.Errorf("scheduling task %q: %w", name, err)
		}
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
		runFn(leoPath, cfgPath, taskName)
	})
	if err != nil {
		return fmt.Errorf("parsing schedule %q: %w", spec, err)
	}

	s.entries[name] = id
	s.tasks[name] = task
	return nil
}

func defaultRunTask(leoPath, cfgPath, taskName string) {
	cmd := exec.Command(leoPath, "run", taskName, "--config", cfgPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cron: task %q failed: %v\nOutput: %s\n", taskName, err, output)
	}
}
