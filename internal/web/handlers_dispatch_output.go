package web

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// handleAPIDispatchOutput returns a non-collecting stream snapshot.
func (s *Server) handleAPIDispatchOutput(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", err)})
		return
	}
	tail := 0
	if raw := r.URL.Query().Get("tail"); raw != "" {
		tail, err = strconv.Atoi(raw)
		if err != nil || tail <= 0 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "tail must be a positive integer"})
			return
		}
	}
	output, err := consult.ReadOutput(cfg.StatePath(), r.PathValue("id"), tail)
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: output})
}
