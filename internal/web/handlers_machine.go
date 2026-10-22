package web

import (
	"net/http"

	"github.com/blackpaw-studio/leo/internal/machine"
)

// CodeMachineStatsUnavailable is the error code when no sampler is wired.
const CodeMachineStatsUnavailable = "machine_stats_unavailable"

// MachineSampler reports whole-machine CPU, memory and disk usage.
type MachineSampler interface {
	Sample() machine.Stats
}

// WithMachineStats serves m on GET /api/v1/machine. Optional; without it the
// route answers 503.
func WithMachineStats(m MachineSampler) Option {
	return func(s *Server) { s.machineStats = m }
}

// handleAPIMachine serves GET /api/v1/machine. A section whose probe failed
// is null in the data, never an error.
func (s *Server) handleAPIMachine(w http.ResponseWriter, _ *http.Request) {
	if s.machineStats == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "machine stats are not available", Code: CodeMachineStatsUnavailable})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: s.machineStats.Sample()})
}
