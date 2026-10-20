package tmux

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResolveOptions are the inputs and seams for Resolve. Nil seams default to
// the real filesystem, a live server probe, and Locate.
type ResolveOptions struct {
	// ManagedPath is the managed marker's tmux_path ("" when unmanaged),
	// shaped <root>/<ver>/tmux.
	ManagedPath string
	// ServerVersion reports the version of the running leo tmux server as
	// seen through bin, or false when no server answers.
	ServerVersion func(bin string) (string, bool)
	ReadDir       func(dir string) ([]fs.DirEntry, error)
	Exists        func(path string) bool
	Locate        func() (string, error)
}

// Resolution is the tmux binary choice for one process.
type Resolution struct {
	// Client is the binary every command against the running (or about to
	// be started) server uses: it matches the server's version.
	Client string
	// Start is the binary that creates a new server, e.g. after a reboot.
	// It differs from Client only while an upgrade is pending.
	Start string
	// Pending describes a version skew that resolves itself at the next
	// server restart. Informational: the daemon logs it, the CLI stays quiet.
	Pending string
	// Err is set when the server's version has no copy to talk to it with.
	// Client and Start fall back to the managed path so callers can still try.
	Err error
}

// Resolve picks the tmux binary. Unmanaged installs use Locate. A managed
// install uses its pinned tmux_path, except while a leo server started from
// an older pinned copy is still running: tmux client and server must match,
// so Client follows the server's version (never kill-server to fix a skew).
func Resolve(opts ResolveOptions) Resolution {
	locate := opts.Locate
	if locate == nil {
		locate = Locate
	}
	if opts.ManagedPath == "" {
		path, err := locate()
		return Resolution{Client: path, Start: path, Err: err}
	}

	probe := opts.ServerVersion
	if probe == nil {
		probe = probeServerVersion
	}
	readDir := opts.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	exists := opts.Exists
	if exists == nil {
		exists = fileExists
	}

	pinnedDir := filepath.Dir(opts.ManagedPath)
	pinned := filepath.Base(pinnedDir)
	root := filepath.Dir(pinnedDir)

	version, ok := probe(opts.ManagedPath)
	if !ok {
		for _, copyPath := range managedCopies(root, pinned, readDir, exists) {
			if version, ok = probe(copyPath); ok {
				break
			}
		}
	}
	managed := Resolution{Client: opts.ManagedPath, Start: opts.ManagedPath}
	if !ok || version == pinned {
		return managed
	}

	serverCopy := filepath.Join(root, version, "tmux")
	if !exists(serverCopy) {
		managed.Err = fmt.Errorf("tmux server is version %s but %s is missing; falling back to %s (attach may fail)", version, serverCopy, opts.ManagedPath)
		return managed
	}
	managed.Client = serverCopy
	managed.Pending = fmt.Sprintf("tmux upgrade pending: server is %s, tmux_path is %s; using %s until the server restarts", version, pinned, serverCopy)
	return managed
}

// managedCopies lists <root>/<ver>/tmux for every version directory other
// than the pinned one, in sorted order.
func managedCopies(root, pinned string, readDir func(string) ([]fs.DirEntry, error), exists func(string) bool) []string {
	entries, err := readDir(root)
	if err != nil {
		return nil
	}
	var copies []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == pinned {
			continue
		}
		if path := filepath.Join(root, entry.Name(), "tmux"); exists(path) {
			copies = append(copies, path)
		}
	}
	sort.Strings(copies)
	return copies
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// probeServerVersion asks the running leo server for its version through
// bin. Format strings expand server-side, so the answer is the server's
// version whichever tmux binary asks.
func probeServerVersion(bin string) (string, bool) {
	out, err := execCommand(context.Background(), bin, Args("display-message", "-p", "#{version}")...).Output()
	if err != nil {
		return "", false
	}
	version := strings.TrimSpace(string(out))
	return version, version != ""
}
