package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// maxSurfaceFileBody bounds the request body; a path plus a 200-rune reason
// is far below it.
const maxSurfaceFileBody = 64 << 10

// surfaceFileRequest is the leo_surface_file MCP tool's call body. DispatchID
// is the caller's LEO_DISPATCH_ID: a dispatched subagent inherits its
// caller's LEO_PROCESS_NAME, so the name alone cannot tell them apart.
type surfaceFileRequest struct {
	Path       string `json:"path"`
	Line       *int   `json:"line,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DispatchID string `json:"dispatch_id,omitempty"`
}

// surfaceError is a rejection with the HTTP status it maps to.
type surfaceError struct {
	status int
	msg    string
}

func (e *surfaceError) Error() string { return e.msg }

func surfaceErrorf(status int, format string, args ...any) *surfaceError {
	return &surfaceError{status: status, msg: fmt.Sprintf(format, args...)}
}

// handleAPIAgentSurfaceFile records a file the named agent pushed to the
// user's attention and announces it on the event bus.
// POST /api/agent/{name}/surface-file
func (s *Server) handleAPIAgentSurfaceFile(w http.ResponseWriter, r *http.Request) {
	id, err := s.surfaceFile(r.PathValue("name"), r.Body)
	if err != nil {
		status := http.StatusInternalServerError
		var se *surfaceError
		if errors.As(err, &se) {
			status = se.status
		}
		writeJSON(w, status, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"id": id}})
}

func (s *Server) surfaceFile(name string, body io.Reader) (string, error) {
	if s.surfacedFiles == nil {
		return "", surfaceErrorf(http.StatusServiceUnavailable, "surfacing files is not available on this daemon")
	}
	req, err := decodeSurfaceRequest(body)
	if err != nil {
		return "", err
	}
	startedAt, workspace, err := s.liveSurfaceAgent(name)
	if err != nil {
		return "", err
	}
	abs, err := resolveSurfacePath(req.Path, workspace)
	if err != nil {
		return "", err
	}
	// The stat above can be slow (network mounts); an agent restarted
	// meanwhile must not have its predecessor's file attributed to it.
	if again, _, err := s.liveSurfaceAgent(name); err != nil || !again.Equal(startedAt) {
		return "", surfaceErrorf(http.StatusConflict, "agent %q restarted while the file was being checked; call again", name)
	}
	in := observe.SurfaceInput{Path: req.Path, AbsPath: abs, Reason: req.Reason}
	if req.Line != nil {
		in.Line = *req.Line
	}
	file, err := s.surfacedFiles.Add(name, startedAt, in)
	if errors.Is(err, observe.ErrStaleIncarnation) {
		return "", surfaceErrorf(http.StatusConflict, "agent %q restarted while the file was being checked; call again", name)
	}
	if err != nil {
		return "", err
	}
	return file.ID, nil
}

// decodeSurfaceRequest parses and validates everything that does not need
// the filesystem or the agent's state.
func decodeSurfaceRequest(body io.Reader) (surfaceFileRequest, error) {
	var req surfaceFileRequest
	if err := json.NewDecoder(io.LimitReader(body, maxSurfaceFileBody)).Decode(&req); err != nil {
		return req, surfaceErrorf(http.StatusBadRequest, "invalid request: %v", err)
	}
	switch {
	case req.DispatchID != "":
		return req, surfaceErrorf(http.StatusForbidden, "leo_surface_file is not available to leo_dispatch subagents (dispatch %s); report the file to your orchestrator instead", req.DispatchID)
	case req.Path == "":
		return req, surfaceErrorf(http.StatusBadRequest, "path is required")
	case req.Line != nil && *req.Line < 1:
		return req, surfaceErrorf(http.StatusBadRequest, "line must be >= 1")
	case utf8.RuneCountInString(req.Reason) > observe.MaxSurfaceReasonRunes:
		return req, surfaceErrorf(http.StatusBadRequest, "reason must be at most %d characters", observe.MaxSurfaceReasonRunes)
	}
	return req, nil
}

// liveSurfaceAgent returns the incarnation and workspace of name, which must
// be a running, supervised ephemeral agent with a persisted record.
func (s *Server) liveSurfaceAgent(name string) (time.Time, string, error) {
	denied := surfaceErrorf(http.StatusForbidden, "only live supervised agents may surface files; %q is not one", name)
	if s.processes == nil || s.agentSvc == nil {
		return time.Time{}, "", denied
	}
	st, ok := s.processes.States()[name]
	if !ok || !st.Ephemeral || st.Status != "running" {
		return time.Time{}, "", denied
	}
	for _, rec := range s.agentSvc.List() {
		if rec.Name == name {
			return st.StartedAt, rec.Workspace, nil
		}
	}
	return time.Time{}, "", denied
}

// resolveSurfacePath resolves path against workspace and requires it to name
// an existing non-directory.
func resolveSurfacePath(path, workspace string) (string, error) {
	abs := path
	if !filepath.IsAbs(path) {
		if workspace == "" {
			return "", surfaceErrorf(http.StatusBadRequest, "relative path %q needs an agent workspace; pass an absolute path", path)
		}
		abs = filepath.Join(workspace, path)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", surfaceErrorf(http.StatusBadRequest, "file not found: %s", abs)
	case err != nil:
		return "", surfaceErrorf(http.StatusBadRequest, "cannot access %s: %v", abs, err)
	case info.IsDir():
		return "", surfaceErrorf(http.StatusBadRequest, "%s is a directory, not a file", abs)
	}
	return abs, nil
}
