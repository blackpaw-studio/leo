package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPidPath(t *testing.T) {
	got := PidPath("/home/user/workspace")
	want := filepath.Join("/home/user/workspace", "state", "service.pid")
	if got != want {
		t.Errorf("PidPath() = %q, want %q", got, want)
	}
}

func TestLogPathFor(t *testing.T) {
	got := LogPathFor("/home/user/workspace")
	want := filepath.Join("/home/user/workspace", "state", "service.log")
	if got != want {
		t.Errorf("LogPathFor() = %q, want %q", got, want)
	}
}

func TestRotateLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "service.log")

	// Create initial log
	os.WriteFile(logPath, []byte("log1"), 0600)

	if err := RotateLog(logPath); err != nil {
		t.Fatalf("RotateLog() error: %v", err)
	}

	// Original should be gone
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Error("original log should be renamed")
	}

	// .1 should exist with original content
	data, err := os.ReadFile(logPath + ".1")
	if err != nil {
		t.Fatalf("reading .1: %v", err)
	}
	if string(data) != "log1" {
		t.Errorf("rotated content = %q, want 'log1'", string(data))
	}
}

func TestRotateLogShifts(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "service.log")

	// Create existing rotated logs
	os.WriteFile(logPath+".1", []byte("old1"), 0600)
	os.WriteFile(logPath, []byte("current"), 0600)

	if err := RotateLog(logPath); err != nil {
		t.Fatalf("RotateLog() error: %v", err)
	}

	// .1 should be current log
	data, _ := os.ReadFile(logPath + ".1")
	if string(data) != "current" {
		t.Errorf(".1 = %q, want 'current'", string(data))
	}

	// .2 should be old .1
	data, _ = os.ReadFile(logPath + ".2")
	if string(data) != "old1" {
		t.Errorf(".2 = %q, want 'old1'", string(data))
	}
}

func TestRotateLogNoFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nonexistent.log")

	// Should be a no-op
	if err := RotateLog(logPath); err != nil {
		t.Fatalf("RotateLog() error on nonexistent: %v", err)
	}
}

func TestStartWritesPidFile(t *testing.T) {
	origStart := startProcess
	origWrite := writeFile
	origRead := readFile
	origMkdir := mkdirAll
	origLog := openLogFile
	defer func() {
		startProcess = origStart
		writeFile = origWrite
		readFile = origRead
		mkdirAll = origMkdir
		openLogFile = origLog
	}()

	openLogFile = func(path string) (*os.File, error) {
		return os.CreateTemp("", "leo-test-log")
	}

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	mkdirAll = func(path string, perm os.FileMode) error {
		return nil
	}

	startProcess = func(leoPath, configPath, workDir string, logFile *os.File) (int, error) {
		if leoPath != "/usr/local/bin/leo" {
			t.Errorf("leoPath = %q, want /usr/local/bin/leo", leoPath)
		}
		if configPath != "/workspace/leo.yaml" {
			t.Errorf("configPath = %q, want /workspace/leo.yaml", configPath)
		}
		return 12345, nil
	}

	var writtenPath string
	var writtenData []byte
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		writtenPath = name
		writtenData = data
		return nil
	}

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/service.log",
	}

	err := Start(sc)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if !strings.HasSuffix(writtenPath, "service.pid") {
		t.Errorf("wrote to %q, want path ending in service.pid", writtenPath)
	}
	if string(writtenData) != "12345" {
		t.Errorf("wrote pid %q, want 12345", string(writtenData))
	}
}

func TestStartAlreadyRunning(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	defer func() {
		readFile = origRead
		findProcess = origFind
	}()

	readFile = func(name string) ([]byte, error) {
		return []byte("99999"), nil
	}
	findProcess = func(pid int) (*os.Process, error) {
		// Return current process so signal check succeeds
		return os.FindProcess(os.Getpid())
	}

	sc := ServiceConfig{
		WorkDir: "/workspace",
	}

	err := Start(sc)
	if err == nil {
		t.Fatal("Start() should error when already running")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want 'already running'", err.Error())
	}
}

func TestStartCleansStalesPid(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	origRemove := removeFile
	origStart := startProcess
	origWrite := writeFile
	origMkdir := mkdirAll
	origLog := openLogFile
	defer func() {
		readFile = origRead
		findProcess = origFind
		removeFile = origRemove
		startProcess = origStart
		writeFile = origWrite
		mkdirAll = origMkdir
		openLogFile = origLog
	}()

	readFile = func(name string) ([]byte, error) {
		return []byte("99999"), nil
	}
	findProcess = func(pid int) (*os.Process, error) {
		return nil, errors.New("process not found")
	}

	var removedPath string
	removeFile = func(name string) error {
		removedPath = name
		return nil
	}
	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	openLogFile = func(path string) (*os.File, error) {
		return os.CreateTemp("", "leo-test-log")
	}
	startProcess = func(leoPath, configPath, workDir string, logFile *os.File) (int, error) {
		return 111, nil
	}
	writeFile = func(name string, data []byte, perm os.FileMode) error { return nil }

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/service.log",
	}

	err := Start(sc)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if !strings.HasSuffix(removedPath, "service.pid") {
		t.Errorf("should have removed stale pid file, removed: %q", removedPath)
	}
}

func TestStopNoPidFile(t *testing.T) {
	origRead := readFile
	defer func() { readFile = origRead }()

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	err := Stop("/workspace")
	if err == nil {
		t.Fatal("Stop() should error when no pid file")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want 'not running'", err.Error())
	}
}

func TestStatusRunning(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	defer func() {
		readFile = origRead
		findProcess = origFind
	}()

	pid := os.Getpid()
	readFile = func(name string) ([]byte, error) {
		return []byte(strconv.Itoa(pid)), nil
	}
	findProcess = func(p int) (*os.Process, error) {
		return os.FindProcess(p)
	}

	status, err := Status("/workspace")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if !strings.Contains(status, "running") {
		t.Errorf("status = %q, want 'running'", status)
	}
}

func TestStatusStopped(t *testing.T) {
	origRead := readFile
	defer func() { readFile = origRead }()

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	status, err := Status("/workspace")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if status != "stopped" {
		t.Errorf("status = %q, want 'stopped'", status)
	}
}

func TestStatusStalePid(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	origRemove := removeFile
	defer func() {
		readFile = origRead
		findProcess = origFind
		removeFile = origRemove
	}()

	readFile = func(name string) ([]byte, error) {
		return []byte("99999"), nil
	}
	findProcess = func(pid int) (*os.Process, error) {
		return nil, errors.New("no such process")
	}
	removeFile = func(name string) error { return nil }

	status, err := Status("/workspace")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if !strings.Contains(status, "stopped") {
		t.Errorf("status = %q, want 'stopped'", status)
	}
}

func TestStopStalePid(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	origRemove := removeFile
	defer func() {
		readFile = origRead
		findProcess = origFind
		removeFile = origRemove
	}()

	readFile = func(name string) ([]byte, error) {
		return []byte("99999"), nil
	}
	findProcess = func(pid int) (*os.Process, error) {
		return nil, errors.New("no such process")
	}

	var removedPath string
	removeFile = func(name string) error {
		removedPath = name
		return nil
	}

	err := Stop("/workspace")
	if err == nil {
		t.Fatal("Stop() should error for stale pid")
	}
	if !strings.Contains(err.Error(), "stale pid") {
		t.Errorf("error = %q, want 'stale pid'", err.Error())
	}
	if !strings.HasSuffix(removedPath, "service.pid") {
		t.Errorf("should have removed stale pid file, removed: %q", removedPath)
	}
}

func TestStopProcessNotFound(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	origRemove := removeFile
	defer func() {
		readFile = origRead
		findProcess = origFind
		removeFile = origRemove
	}()

	readFile = func(name string) ([]byte, error) {
		return []byte(strconv.Itoa(os.Getpid())), nil
	}

	// First call to isRunning (via findProcess) returns true (running),
	// then findProcess in Stop itself returns error
	callCount := 0
	findProcess = func(pid int) (*os.Process, error) {
		callCount++
		if callCount <= 1 {
			// isRunning check succeeds
			return os.FindProcess(pid)
		}
		// findProcess in Stop returns error
		return nil, fmt.Errorf("process not found")
	}

	removeFile = func(name string) error { return nil }

	err := Stop("/workspace")
	if err == nil {
		t.Fatal("Stop() should error when process not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestStartProcessError(t *testing.T) {
	origStart := startProcess
	origRead := readFile
	origMkdir := mkdirAll
	origLog := openLogFile
	defer func() {
		startProcess = origStart
		readFile = origRead
		mkdirAll = origMkdir
		openLogFile = origLog
	}()

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	openLogFile = func(path string) (*os.File, error) {
		return os.CreateTemp("", "leo-test-log")
	}
	startProcess = func(leoPath, configPath, workDir string, logFile *os.File) (int, error) {
		return 0, fmt.Errorf("exec failed")
	}

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/service.log",
	}

	err := Start(sc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "starting process") {
		t.Errorf("error = %q, want mention of starting process", err.Error())
	}
}

func TestStartLogFileError(t *testing.T) {
	origRead := readFile
	origMkdir := mkdirAll
	origLog := openLogFile
	defer func() {
		readFile = origRead
		mkdirAll = origMkdir
		openLogFile = origLog
	}()

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	openLogFile = func(path string) (*os.File, error) {
		return nil, fmt.Errorf("disk full")
	}

	sc := ServiceConfig{
		WorkDir: "/workspace",
		LogPath: "/workspace/state/service.log",
	}

	err := Start(sc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "opening log file") {
		t.Errorf("error = %q, want mention of opening log file", err.Error())
	}
}

func TestRunSupervisedDelegates(t *testing.T) {
	origFn := supervisedExecFn
	defer func() { supervisedExecFn = origFn }()

	var calledPath string
	var calledArgs []string
	var calledConfigPath string
	supervisedExecFn = func(claudePath string, claudeArgs []string, workDir, configPath string) error {
		calledPath = claudePath
		calledArgs = claudeArgs
		calledConfigPath = configPath
		return nil
	}

	err := RunSupervised("/usr/bin/claude", []string{"--add-dir", "/workspace"}, "/workspace", "/workspace/leo.yaml")
	if err != nil {
		t.Fatalf("RunSupervised() error: %v", err)
	}
	if calledPath != "/usr/bin/claude" {
		t.Errorf("path = %q, want /usr/bin/claude", calledPath)
	}
	if len(calledArgs) != 2 || calledArgs[0] != "--add-dir" {
		t.Errorf("args = %v, want [--add-dir /workspace]", calledArgs)
	}
	if calledConfigPath != "/workspace/leo.yaml" {
		t.Errorf("configPath = %q, want /workspace/leo.yaml", calledConfigPath)
	}
}

func TestStripResumeArg(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "removes resume and value",
			args: []string{"--add-dir", "/workspace", "--resume", "abc-123", "--model", "sonnet"},
			want: []string{"--add-dir", "/workspace", "--model", "sonnet"},
		},
		{
			name: "no resume present",
			args: []string{"--add-dir", "/workspace", "--session-id", "abc-123"},
			want: []string{"--add-dir", "/workspace", "--session-id", "abc-123"},
		},
		{
			name: "resume at end without value",
			args: []string{"--add-dir", "/workspace", "--resume"},
			want: []string{"--add-dir", "/workspace", "--resume"},
		},
		{
			name: "empty args",
			args: []string{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripResumeArg(tt.args)
			if len(got) != len(tt.want) {
				t.Errorf("stripResumeArg(%v) = %v, want %v", tt.args, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("stripResumeArg(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestClearSessionStore(t *testing.T) {
	origWrite := writeFile
	defer func() { writeFile = origWrite }()

	var writtenPath string
	var writtenData []byte
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		writtenPath = name
		writtenData = data
		return nil
	}

	clearSessionStore("/workspace")

	wantPath := filepath.Join("/workspace", "state", "sessions.json")
	if writtenPath != wantPath {
		t.Errorf("path = %q, want %q", writtenPath, wantPath)
	}
	if string(writtenData) != "{}" {
		t.Errorf("data = %q, want {}", string(writtenData))
	}
}

func TestCleanupOrphanedPlugins_StaleLock(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	origRemove := removeFile
	origHome := os.Getenv("HOME")
	defer func() {
		readFile = origRead
		findProcess = origFind
		removeFile = origRemove
		os.Setenv("HOME", origHome)
	}()

	os.Setenv("HOME", "/fakehome")

	readFile = func(name string) ([]byte, error) {
		return []byte("99999"), nil
	}
	findProcess = func(pid int) (*os.Process, error) {
		return nil, errors.New("no such process")
	}

	var removedPath string
	removeFile = func(name string) error {
		removedPath = name
		return nil
	}

	cleanupOrphanedPlugins()

	wantPath := filepath.Join("/fakehome", ".claude", "channels", "telegram", "data", "telegram.lock")
	if removedPath != wantPath {
		t.Errorf("removed %q, want %q", removedPath, wantPath)
	}
}

func TestCleanupOrphanedPlugins_NoLockFile(t *testing.T) {
	origRead := readFile
	origRemove := removeFile
	defer func() {
		readFile = origRead
		removeFile = origRemove
	}()

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	removeCalled := false
	removeFile = func(name string) error {
		removeCalled = true
		return nil
	}

	cleanupOrphanedPlugins()

	if removeCalled {
		t.Error("should not remove anything when no lock file exists")
	}
}

func TestCleanupOrphanedPlugins_InvalidLockContent(t *testing.T) {
	origRead := readFile
	origRemove := removeFile
	origHome := os.Getenv("HOME")
	defer func() {
		readFile = origRead
		removeFile = origRemove
		os.Setenv("HOME", origHome)
	}()

	os.Setenv("HOME", "/fakehome")

	readFile = func(name string) ([]byte, error) {
		return []byte("not-a-number"), nil
	}

	var removedPath string
	removeFile = func(name string) error {
		removedPath = name
		return nil
	}

	cleanupOrphanedPlugins()

	wantPath := filepath.Join("/fakehome", ".claude", "channels", "telegram", "data", "telegram.lock")
	if removedPath != wantPath {
		t.Errorf("removed %q, want %q (should remove lock with invalid content)", removedPath, wantPath)
	}
}

func TestStartMkdirError(t *testing.T) {
	origRead := readFile
	origMkdir := mkdirAll
	defer func() {
		readFile = origRead
		mkdirAll = origMkdir
	}()

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	mkdirAll = func(path string, perm os.FileMode) error {
		return fmt.Errorf("permission denied")
	}

	sc := ServiceConfig{
		WorkDir: "/workspace",
		LogPath: "/workspace/state/service.log",
	}

	err := Start(sc)
	if err == nil {
		t.Fatal("expected error when mkdir fails")
	}
	if !strings.Contains(err.Error(), "creating state directory") {
		t.Errorf("error = %q, want mention of state directory", err.Error())
	}
}

func TestStartWritePidError(t *testing.T) {
	origStart := startProcess
	origWrite := writeFile
	origRead := readFile
	origMkdir := mkdirAll
	origLog := openLogFile
	defer func() {
		startProcess = origStart
		writeFile = origWrite
		readFile = origRead
		mkdirAll = origMkdir
		openLogFile = origLog
	}()

	readFile = func(name string) ([]byte, error) { return nil, os.ErrNotExist }
	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	openLogFile = func(path string) (*os.File, error) {
		return os.CreateTemp("", "leo-test-log")
	}
	startProcess = func(leoPath, configPath, workDir string, logFile *os.File) (int, error) {
		return 12345, nil
	}
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		return fmt.Errorf("disk full")
	}

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/service.log",
	}

	err := Start(sc)
	if err == nil {
		t.Fatal("expected error when writing pid file fails")
	}
	if !strings.Contains(err.Error(), "writing pid file") {
		t.Errorf("error = %q, want mention of writing pid file", err.Error())
	}
}

func TestCleanupOrphanedPlugins_AliveProcess(t *testing.T) {
	origRead := readFile
	origFind := findProcess
	origRemove := removeFile
	origHome := os.Getenv("HOME")
	defer func() {
		readFile = origRead
		findProcess = origFind
		removeFile = origRemove
		os.Setenv("HOME", origHome)
	}()

	os.Setenv("HOME", "/fakehome")

	// Lock file references current process (which is alive)
	readFile = func(name string) ([]byte, error) {
		return []byte(strconv.Itoa(os.Getpid())), nil
	}
	findProcess = func(pid int) (*os.Process, error) {
		return os.FindProcess(pid)
	}

	removeCalled := false
	removeFile = func(name string) error {
		removeCalled = true
		return nil
	}

	cleanupOrphanedPlugins()

	if removeCalled {
		t.Error("should not remove lock when process is alive")
	}
}
