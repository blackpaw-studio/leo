package bridgemod

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// psTimeout bounds the process listing PruneMods relies on.
const psTimeout = 5 * time.Second

// ProcessLister returns the command lines of the processes running now.
type ProcessLister func(ctx context.Context) ([]string, error)

// ListProcesses lists every process's command line with ps, unwrapped.
func ListProcesses(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, psTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-A", "-ww", "-o", "command=")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bridgemod: listing processes: %w", err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}

// errNoProcesses is an empty process listing: nothing can be told from it.
var errNoProcesses = errors.New("bridgemod: the process listing was empty")

// PruneMods removes the mods Materialize left under stateDir by earlier leo
// versions, keeping current (this daemon's) and the newest other one (an
// agent of the version before may still run on it). A mod named in a
// running process's command line (a claude loaded it with --plugin-dir) is
// kept however old. When the processes cannot be listed nothing is
// removed. Returns the dirs it removed.
func PruneMods(ctx context.Context, stateDir, current string, list ProcessLister) ([]string, error) {
	parent := filepath.Join(stateDir, "mods", Name)
	stale, err := staleMods(parent, filepath.Base(current))
	if err != nil || len(stale) == 0 {
		return nil, err
	}
	running, err := list(ctx)
	if err != nil {
		return nil, err
	}
	if len(running) == 0 {
		return nil, errNoProcesses
	}
	var removed []string
	var errs []error
	for _, name := range stale {
		if inUse(running, name) {
			continue
		}
		dir := filepath.Join(parent, name)
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("bridgemod: removing %s: %w", dir, err))
			continue
		}
		removed = append(removed, dir)
	}
	return removed, errors.Join(errs...)
}

// staleMods are the mod dirs under parent other than current and the
// newest other one. Temp dirs (an install in progress) are not mods.
func staleMods(parent, current string) ([]string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("bridgemod: listing %s: %w", parent, err)
	}
	type mod struct {
		name string
		at   time.Time
	}
	var others []mod
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == current {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("bridgemod: reading %s: %w", e.Name(), err)
		}
		others = append(others, mod{e.Name(), info.ModTime()})
	}
	if len(others) <= 1 {
		return nil, nil
	}
	sort.Slice(others, func(i, j int) bool { return others[i].at.After(others[j].at) })
	stale := make([]string, 0, len(others)-1)
	for _, m := range others[1:] {
		stale = append(stale, m.name)
	}
	return stale, nil
}

// inUse reports whether any command line names the mod dir called name.
// The dir's own name (version and content hash) under mods/leo-bridge
// identifies it whichever spelling of the state path launched it.
func inUse(commandLines []string, name string) bool {
	needle := string(filepath.Separator) + Name + string(filepath.Separator) + name
	for _, line := range commandLines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}
