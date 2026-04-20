package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/natefinch/lumberjack.v2"
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

func TestNewRotatingLogWriter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "service.log")

	w := NewRotatingLogWriter(logPath)
	defer func() { _ = w.Close() }()

	lj, ok := w.(*lumberjack.Logger)
	if !ok {
		t.Fatalf("NewRotatingLogWriter returned %T, want *lumberjack.Logger", w)
	}
	if lj.Filename != logPath {
		t.Errorf("Filename = %q, want %q", lj.Filename, logPath)
	}
	if lj.MaxSize != logMaxSizeMB {
		t.Errorf("MaxSize = %d, want %d", lj.MaxSize, logMaxSizeMB)
	}
	if lj.MaxBackups != logMaxBackups {
		t.Errorf("MaxBackups = %d, want %d", lj.MaxBackups, logMaxBackups)
	}
	if lj.MaxAge != logMaxAgeDays {
		t.Errorf("MaxAge = %d, want %d", lj.MaxAge, logMaxAgeDays)
	}
	if lj.Compress != logCompress {
		t.Errorf("Compress = %v, want %v", lj.Compress, logCompress)
	}

	// Smoke-test: the writer accepts writes.
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if string(data) != "hello\n" {
		t.Errorf("log content = %q, want %q", string(data), "hello\n")
	}
}

func TestRotatingLogWriterRotatesOnSize(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "service.log")

	// Use lumberjack directly with a tiny MaxSize so the test doesn't
	// need to write 10 MB to trigger rotation.
	w := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    1, // 1 MB
		MaxBackups: 2,
		Compress:   false,
	}
	defer func() { _ = w.Close() }()

	payload := make([]byte, 512*1024) // 512 KB
	for i := 0; i < 4; i++ {
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) < 2 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected at least one rotated backup, got %v", names)
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
	var calledProcesses []ProcessSpec
	var calledConfigPath string
	var calledToken string
	supervisedExecFn = func(claudePath string, processes []ProcessSpec, homePath, configPath, webToken string) error {
		calledPath = claudePath
		calledProcesses = processes
		calledConfigPath = configPath
		calledToken = webToken
		return nil
	}

	specs := []ProcessSpec{
		{Name: "assistant", ClaudeArgs: []string{"--add-dir", "/workspace"}, WorkDir: "/workspace"},
	}
	const wantToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	err := RunSupervised("/usr/bin/claude", specs, "/home/.leo", "/home/.leo/leo.yaml", wantToken)
	if err != nil {
		t.Fatalf("RunSupervised() error: %v", err)
	}
	if calledPath != "/usr/bin/claude" {
		t.Errorf("path = %q, want /usr/bin/claude", calledPath)
	}
	if len(calledProcesses) != 1 || calledProcesses[0].Name != "assistant" {
		t.Errorf("processes = %v, want 1 process named assistant", calledProcesses)
	}
	if calledConfigPath != "/home/.leo/leo.yaml" {
		t.Errorf("configPath = %q, want /home/.leo/leo.yaml", calledConfigPath)
	}
	if calledToken != wantToken {
		t.Errorf("webToken = %q, want %q", calledToken, wantToken)
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

func TestClearProcessSession(t *testing.T) {
	origRead := readFile
	origWrite := writeFile
	defer func() { readFile = origRead; writeFile = origWrite }()

	// Simulate existing sessions.json with two entries
	readFile = func(name string) ([]byte, error) {
		return []byte(`{"process:assistant":"sid1","process:researcher":"sid2"}`), nil
	}

	var writtenPath string
	var writtenData []byte
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		writtenPath = name
		writtenData = data
		return nil
	}

	clearProcessSession("/home/.leo", "assistant")

	wantPath := filepath.Join("/home/.leo", "state", "sessions.json")
	if writtenPath != wantPath {
		t.Errorf("path = %q, want %q", writtenPath, wantPath)
	}
	// Should only remove process:assistant, keeping process:researcher
	if string(writtenData) != `{"process:researcher":"sid2"}` {
		t.Errorf("data = %q, want researcher session preserved", string(writtenData))
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"it's", "'it'\\''s'"},
		{"$(evil)", "'$(evil)'"},
		{"; rm -rf /", "'; rm -rf /'"},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.input); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
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

func TestHasDevChannelFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"no flags", nil, false},
		{"only channels", []string{"--channels", "plugin:x@y"}, false},
		{"dev flag present", []string{"--model", "sonnet", "--dangerously-load-development-channels", "plugin:x@y"}, true},
		{"dev flag only", []string{"--dangerously-load-development-channels", "plugin:x@y"}, true},
		{"mixed", []string{"--channels", "plugin:a@b", "--dangerously-load-development-channels", "plugin:x@y"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDevChannelFlag(tt.args); got != tt.want {
				t.Errorf("hasDevChannelFlag(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
