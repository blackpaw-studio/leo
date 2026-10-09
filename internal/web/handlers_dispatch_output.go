package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// handleAPIDispatchOutput returns a stream snapshot. It collects nothing; the one side effect is
// releasing a finished release_on_finish run whose result the caller just read.
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
	// A finished release_on_finish run is released once a caller has read its
	// result; the snapshot itself stays side-effect free for every other run.
	if err := s.consults.ReleaseDelivered(output.ID); err != nil {
		log.Printf("dispatch %s: release after output: %v", output.ID, err)
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: output})
}
