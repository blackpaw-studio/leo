package daemon

import (
	"net/http"

	"github.com/blackpaw-studio/leo/internal/machine"
)

// MachineSampler reports whole-machine CPU, memory and disk usage. The
// concrete *machine.Sampler caches, so both the socket and web routes can
// share one.
type MachineSampler interface {
	Sample() machine.Stats
}

// WithMachineStats serves m on GET /machine and, via StartWeb, on
// GET /api/v1/machine. Without it both routes answer 503.
func WithMachineStats(m MachineSampler) Option {
	return func(s *Server) { s.machineStats = m }
}

// handleMachine serves GET /machine: the socket twin of GET /api/v1/machine.
// A section whose probe failed is null in the data, never an error.
func (s *Server) handleMachine(w http.ResponseWriter, _ *http.Request) {
	if s.machineStats == nil {
		writeJSON(w, http.StatusServiceUnavailable, Response{Error: "machine stats are not available", Code: ErrorCodeMachineStatsUnavailable})
		return
	}
	writeData(w, http.StatusOK, s.machineStats.Sample())
}
