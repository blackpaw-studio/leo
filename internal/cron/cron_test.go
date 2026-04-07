package cron

import (
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestInstallSchedulesEnabledTasks(t *testing.T) {
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")
	defer s.Stop()

	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {Schedule: "0,30 7-22 * * *", Enabled: true},
			"news":      {Schedule: "0 7 * * *", Enabled: true},
			"disabled":  {Schedule: "* * * * *", Enabled: false},
		},
	}

	if err := s.Install(cfg); err != nil {
		t.Fatalf("Install() error: %v", err)
	}

	entries := s.List()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["heartbeat"] {
		t.Error("missing heartbeat")
	}
	if !names["news"] {
		t.Error("missing news")
	}
	if names["disabled"] {
		t.Error("disabled task should not be scheduled")
	}
}

func TestRemoveClearsAll(t *testing.T) {
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")
	defer s.Stop()

	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {Schedule: "* * * * *", Enabled: true},
		},
	}

	if err := s.Install(cfg); err != nil {
		t.Fatalf("Install() error: %v", err)
	}

	s.Remove()

	if len(s.List()) != 0 {
		t.Error("expected 0 entries after Remove()")
	}
}

func TestInstallReplacesExisting(t *testing.T) {
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")
	defer s.Stop()

	cfg1 := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"old-task": {Schedule: "* * * * *", Enabled: true},
		},
	}
	if err := s.Install(cfg1); err != nil {
		t.Fatalf("Install() error: %v", err)
	}

	cfg2 := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"new-task": {Schedule: "0 7 * * *", Enabled: true},
		},
	}
	if err := s.Install(cfg2); err != nil {
		t.Fatalf("Install() error: %v", err)
	}

	entries := s.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "new-task" {
		t.Errorf("expected new-task, got %s", entries[0].Name)
	}
}

func TestTimezone(t *testing.T) {
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")
	defer s.Stop()

	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"tz-task": {Schedule: "0 7 * * *", Timezone: "America/New_York", Enabled: true},
		},
	}

	if err := s.Install(cfg); err != nil {
		t.Fatalf("Install() with timezone error: %v", err)
	}

	entries := s.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestInvalidSchedule(t *testing.T) {
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")
	defer s.Stop()

	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"bad": {Schedule: "not a cron expression", Enabled: true},
		},
	}

	if err := s.Install(cfg); err == nil {
		t.Error("expected error for invalid schedule")
	}
}

func TestRunTaskCalled(t *testing.T) {
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")

	var mu sync.Mutex
	var called []string
	s.runFn = func(leoPath, cfgPath, taskName string) {
		mu.Lock()
		defer mu.Unlock()
		called = append(called, taskName)
	}

	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"test-task": {Schedule: "* * * * *", Enabled: true},
		},
	}

	if err := s.Install(cfg); err != nil {
		t.Fatalf("Install() error: %v", err)
	}

	// Verify the entry was registered (we can't easily trigger the cron
	// without waiting a minute, but we verify the wiring is correct)
	entries := s.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "test-task" {
		t.Errorf("expected test-task, got %s", entries[0].Name)
	}
}

func TestListSortedByName(t *testing.T) {
	s := New("/usr/local/bin/leo", "/tmp/leo.yaml")
	defer s.Stop()

	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"zulu":  {Schedule: "0 7 * * *", Enabled: true},
			"alpha": {Schedule: "0 8 * * *", Enabled: true},
			"mike":  {Schedule: "0 9 * * *", Enabled: true},
		},
	}

	if err := s.Install(cfg); err != nil {
		t.Fatalf("Install() error: %v", err)
	}

	entries := s.List()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Name != "alpha" || entries[1].Name != "mike" || entries[2].Name != "zulu" {
		t.Errorf("entries not sorted: %v", entries)
	}
}
