package web

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// fileServeWriteTimeout replaces the server's 30 s WriteTimeout for one file
// download, so a large file over a slow phone link is not cut off.
const fileServeWriteTimeout = 10 * time.Minute

// handleAPIFile streams a surfaced file by its surfaced-file id. It never
// accepts a path: the id resolves, via the in-memory store, to the path the
// agent surfaced, and the file is re-checked at serve time. HEAD and Range
// come from http.ServeContent.
// GET|HEAD /api/v1/files/{id}
func (s *Server) handleAPIFile(w http.ResponseWriter, r *http.Request) {
	file, ok := s.surfacedFiles.Find(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: "no such surfaced file (it may have been evicted)"})
		return
	}
	// O_NONBLOCK: a path swapped for a FIFO since it was surfaced must not
	// hang the open. The type check runs on the opened descriptor, so it
	// covers exactly what will be read.
	f, err := os.OpenFile(file.AbsPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: "surfaced file no longer exists"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "surfaced file is not readable"})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "stat failed"})
		return
	}
	if !info.Mode().IsRegular() {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "surfaced path is not a regular file"})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(fileServeWriteTimeout))
	name := filepath.Base(file.AbsPath)
	h := w.Header()
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	// Agent-written content: never let it run as a page on this origin.
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
