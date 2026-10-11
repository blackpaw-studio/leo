// Package testenv isolates a test binary from the developer's real home.
//
// Production code resolves claude's state (~/.claude.json, ~/.claude/),
// codex's (~/.codex/config.toml) and leo's own home (~/.leo) from HOME,
// CLAUDE_CONFIG_DIR and CODEX_HOME. A test that reaches those paths without
// pointing them somewhere private writes into the real files (issue #203).
// Every package with tests calls Main from its TestMain.
package testenv

import (
	"fmt"
	"os"
	"testing"
)

// homeOverrides are the variables that redirect where claude and codex keep
// their state. They are cleared so tests exercise the HOME-derived defaults;
// tests that need them set them with t.Setenv.
var homeOverrides = []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"}

// Main runs m with HOME pointing at a private temp dir and returns the exit
// code for os.Exit. Use it as: func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }.
func Main(m *testing.M) int {
	restore, err := Isolate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolating home: %v\n", err)
		return 1
	}
	defer restore()
	return m.Run()
}

// Isolate points HOME at a fresh temp dir and clears CLAUDE_CONFIG_DIR and
// CODEX_HOME. The returned func restores the previous environment and removes
// the dir.
func Isolate() (restore func(), err error) {
	home, err := os.MkdirTemp("", "leo-test-home-")
	if err != nil {
		return nil, err
	}
	saved := snapshot(append([]string{"HOME"}, homeOverrides...))
	restore = func() {
		saved.apply()
		_ = os.RemoveAll(home)
	}
	if err := os.Setenv("HOME", home); err != nil {
		restore()
		return nil, err
	}
	for _, k := range homeOverrides {
		if err := os.Unsetenv(k); err != nil {
			restore()
			return nil, err
		}
	}
	return restore, nil
}

type envSnapshot map[string]*string

func snapshot(keys []string) envSnapshot {
	s := envSnapshot{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			s[k] = &v
		} else {
			s[k] = nil
		}
	}
	return s
}

func (s envSnapshot) apply() {
	for k, v := range s {
		if v == nil {
			_ = os.Unsetenv(k)
		} else {
			_ = os.Setenv(k, *v)
		}
	}
}
