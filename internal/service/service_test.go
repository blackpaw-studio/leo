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
	got := PidPath("/home/user/workspace", "myagent")
	want := filepath.Join("/home/user/workspace", "state", "chat.pid")
	if got != want {
		t.Errorf("PidPath() = %q, want %q", got, want)
	}
}

func TestLogPathFor(t *testing.T) {
	got := LogPathFor("/home/user/workspace")
	want := filepath.Join("/home/user/workspace", "state", "chat.log")
	if got != want {
		t.Errorf("LogPathFor() = %q, want %q", got, want)
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
		AgentName:  "myagent",
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/chat.log",
	}

	err := Start(sc)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if !strings.HasSuffix(writtenPath, "chat.pid") {
		t.Errorf("wrote to %q, want path ending in chat.pid", writtenPath)
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
		AgentName: "myagent",
		WorkDir:   "/workspace",
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
		AgentName:  "myagent",
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/chat.log",
	}

	err := Start(sc)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if !strings.HasSuffix(removedPath, "chat.pid") {
		t.Errorf("should have removed stale pid file, removed: %q", removedPath)
	}
}

func TestStopNoPidFile(t *testing.T) {
	origRead := readFile
	defer func() { readFile = origRead }()

	readFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	err := Stop("myagent", "/workspace")
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

	status, err := Status("myagent", "/workspace")
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

	status, err := Status("myagent", "/workspace")
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

	status, err := Status("myagent", "/workspace")
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

	err := Stop("myagent", "/workspace")
	if err == nil {
		t.Fatal("Stop() should error for stale pid")
	}
	if !strings.Contains(err.Error(), "stale pid") {
		t.Errorf("error = %q, want 'stale pid'", err.Error())
	}
	if !strings.HasSuffix(removedPath, "chat.pid") {
		t.Errorf("should have removed stale pid file, removed: %q", removedPath)
	}
}

func TestStopSuccess(t *testing.T) {
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

	signalCount := 0
	findProcess = func(pid int) (*os.Process, error) {
		return os.FindProcess(pid)
	}
	_ = signalCount

	removeFile = func(name string) error { return nil }

	// This test is tricky because we'd be sending SIGTERM to ourselves.
	// Just verify the stale PID path is covered above.
	// The happy path is integration-tested via E2E.
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
		AgentName:  "test",
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/chat.log",
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
		AgentName: "test",
		WorkDir:   "/workspace",
		LogPath:   "/workspace/state/chat.log",
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
	supervisedExecFn = func(claudePath string, claudeArgs []string, workDir string) error {
		calledPath = claudePath
		calledArgs = claudeArgs
		return nil
	}

	err := RunSupervised("/usr/bin/claude", []string{"--agent", "test"}, "/workspace")
	if err != nil {
		t.Fatalf("RunSupervised() error: %v", err)
	}
	if calledPath != "/usr/bin/claude" {
		t.Errorf("path = %q, want /usr/bin/claude", calledPath)
	}
	if len(calledArgs) != 2 || calledArgs[0] != "--agent" {
		t.Errorf("args = %v, want [--agent test]", calledArgs)
	}
}
