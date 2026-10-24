package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"time"
)

// maxDumpSuffix bounds the same-second collision search.
const maxDumpSuffix = 1000

// goroutineDumper writes every goroutine's stack to a file in dir.
type goroutineDumper struct {
	dir  string
	now  func() time.Time
	logf func(format string, args ...any)
}

// dump writes <dir>/goroutines-<unix>.txt (mode 0600) and logs its path. The
// stacks go to a dot-prefixed temp file first and are hard-linked to the final
// name, so the final name only appears once the file is complete and an
// earlier dump from the same second is never overwritten (it gets -2, -3, ...).
func (g goroutineDumper) dump() (string, error) {
	if err := os.MkdirAll(g.dir, 0o700); err != nil {
		return "", fmt.Errorf("creating dump dir: %w", err)
	}
	tmp, err := os.CreateTemp(g.dir, ".goroutines-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating dump file: %w", err)
	}
	defer os.Remove(tmp.Name())
	writeErr := pprof.Lookup("goroutine").WriteTo(tmp, 2)
	if closeErr := tmp.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return "", fmt.Errorf("writing goroutine dump: %w", writeErr)
	}

	stamp := g.now().Unix()
	for n := 1; n <= maxDumpSuffix; n++ {
		name := fmt.Sprintf("goroutines-%d.txt", stamp)
		if n > 1 {
			name = fmt.Sprintf("goroutines-%d-%d.txt", stamp, n)
		}
		path := filepath.Join(g.dir, name)
		err := os.Link(tmp.Name(), path)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("publishing goroutine dump: %w", err)
		}
		g.logf("goroutine dump written to %s", path)
		return path, nil
	}
	return "", fmt.Errorf("publishing goroutine dump: %d dumps already share second %d", maxDumpSuffix, stamp)
}

// serveDumpSignals runs dump once per signal on sigs until done closes. It
// handles one signal at a time; the caller's buffered channel merges bursts.
func serveDumpSignals(sigs <-chan os.Signal, done <-chan struct{}, dump func()) {
	for {
		select {
		case <-sigs:
			dump()
		case <-done:
			return
		}
	}
}

// installGoroutineDump makes SIGUSR1 write a goroutine dump into dir without
// otherwise affecting the daemon, and returns the func that ends the handler
// goroutine. signal.Notify is deliberately never undone: once it is, SIGUSR1's
// default action (terminate) applies again, so a late `leo service dump`
// during shutdown would kill a daemon that is merely slow to stop.
func installGoroutineDump(dir string, now func() time.Time, logf func(string, ...any)) (stop func()) {
	dumper := goroutineDumper{dir: dir, now: now, logf: logf}
	sigs := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sigs, syscall.SIGUSR1)
	go serveDumpSignals(sigs, done, func() {
		if _, err := dumper.dump(); err != nil {
			logf("goroutine dump failed: %v", err)
		}
	})
	return func() { close(done) }
}
