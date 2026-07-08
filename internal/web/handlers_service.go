package web

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// serviceLogTailDefaultLines and serviceLogTailMaxLines bound the /web/service/logtail
// endpoint: default when n is absent/invalid, and the hard cap regardless of
// what the caller requests, so a runaway ?n= can't force reading/joining an
// unbounded number of lines on every poll.
const (
	serviceLogTailDefaultLines = 200
	serviceLogTailMaxLines     = 1000

	// serviceLogTailReadWindow bounds how much of a large log file we read
	// from the tail before splitting into lines — avoids loading an entire
	// multi-GB log into memory just to keep the last few hundred lines.
	serviceLogTailReadWindow = 5 << 20 // 5MB
)

// logTailTemplate renders tail text inside a <pre> wrapper. html/template
// auto-escapes the "." value in HTML context, so any HTML-looking content
// captured from process output (e.g. a session accidentally echoing a
// <script> tag) is rendered inert rather than executed.
var logTailTemplate = template.Must(template.New("logtail").Parse(`<pre class="logtail">{{.}}</pre>`))

// logTailDimTemplate is used for the friendly "no log yet" / soft-error
// states. It still goes through html/template even though today's messages
// are static, so future callers can't accidentally reintroduce raw
// interpolation here.
var logTailDimTemplate = template.Must(template.New("logtail-dim").Parse(`<pre class="logtail dim">{{.}}</pre>`))

// serviceLogPath returns the path to the service log for a given leo home
// directory. This mirrors service.LogPathFor(homePath) exactly
// (filepath.Join(homePath, "state", "service.log")); it is duplicated here
// rather than imported because internal/service imports internal/daemon,
// which imports internal/web to embed this UI — importing internal/service
// from internal/web would create an import cycle.
func serviceLogPath(homePath string) string {
	return filepath.Join(homePath, "state", "service.log")
}

// handleServiceLogTail serves the last N lines of the service log as an
// HTML fragment for the Service page's log viewer (initial load + manual
// refresh button).
func (s *Server) handleServiceLogTail(w http.ResponseWriter, r *http.Request) {
	n := serviceLogTailDefaultLines
	if raw := r.URL.Query().Get("n"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n > serviceLogTailMaxLines {
		n = serviceLogTailMaxLines
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	cfg, err := s.loadConfig()
	if err != nil {
		logTailDimTemplate.Execute(w, fmt.Sprintf("failed to load config: %v", err)) //nolint:errcheck
		return
	}

	logPath := serviceLogPath(cfg.HomePath)
	tail, err := readLogTail(logPath, n)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logTailDimTemplate.Execute(w, "no log file yet") //nolint:errcheck
			return
		}
		logTailDimTemplate.Execute(w, fmt.Sprintf("failed to read log: %v", err)) //nolint:errcheck
		return
	}

	logTailTemplate.Execute(w, tail) //nolint:errcheck
}

// readLogTail returns the last n lines of the file at path. It only reads
// the final serviceLogTailReadWindow bytes of large files rather than the
// whole thing, since the log viewer only ever wants a short tail.
func readLogTail(path string, n int) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is derived from server config, not user input
	if err != nil {
		return "", fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close() //nolint:errcheck

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat log file: %w", err)
	}

	if info.Size() > serviceLogTailReadWindow {
		if _, err := f.Seek(-serviceLogTailReadWindow, io.SeekEnd); err != nil {
			return "", fmt.Errorf("seeking log file: %w", err)
		}
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("reading log file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	// Drop a trailing empty element produced by a trailing newline so it
	// doesn't count as a blank "line" when taking the last n.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n"), nil
}
