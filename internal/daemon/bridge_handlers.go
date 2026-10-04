package daemon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
)

const (
	// bridgeWriteTimeout bounds one command write, so a mod that stops
	// reading cannot pin its handler forever.
	bridgeWriteTimeout = 30 * time.Second
	// maxBridgeReportBytes caps a report body. Most reports are small, but
	// turn.start echoes the prompt and turn.complete the final message
	// (the mod caps each at 1M chars, at most ~6 MiB of escaped JSON); the
	// socket mux has no global cap.
	maxBridgeReportBytes int64 = 16 << 20
)

// MaxBridgeReportBytes is the largest report body the daemon takes;
// `leo bridge report` refuses anything larger before posting.
const MaxBridgeReportBytes = maxBridgeReportBytes

// Bridge returns the hub served on /api/bridge/*, for the call sites that
// deliver through the claude mod bridge.
func (s *Server) Bridge() *bridge.Hub { return s.bridge }

// handleBridgeStream holds agent's command stream open, writing one JSON
// command per line and flushing each. It ends when a newer connection
// replaces it, the hub closes (daemon shutdown), or the client goes away;
// unacked commands stay queued for the next connection either way.
func (s *Server) handleBridgeStream(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if !config.ValidName(agent) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid agent name %q", agent))
		return
	}
	stream, err := s.bridge.Connect(agent)
	if err != nil {
		writeError(w, bridgeErrorStatus(err, http.StatusServiceUnavailable), err.Error())
		return
	}
	defer stream.Close()
	fmt.Fprintf(os.Stderr, "bridge: %s connected\n", agent)

	// The mux's 30s WriteTimeout is a deadline from the request's start, so
	// it would fail the first command written after it. Each write instead
	// gets its own deadline (writeBridgeLine). No read deadline matters
	// here: net/http clears it before running the handler.
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	if err := writeBridgeLine(rc, w, nil); err != nil {
		fmt.Fprintf(os.Stderr, "bridge: %s stream ended: %v\n", agent, err)
		return
	}
	for {
		cmd, err := stream.Next(r.Context())
		if err == nil {
			err = writeBridgeCommand(rc, w, cmd)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "bridge: %s stream ended: %v\n", agent, err)
			return
		}
	}
}

func writeBridgeCommand(rc *http.ResponseController, w io.Writer, cmd bridge.Command) error {
	line, err := cmd.MarshalLine()
	if err != nil {
		return err
	}
	return writeBridgeLine(rc, w, line)
}

// writeBridgeLine writes line (none, to flush headers only) and flushes it
// to the client under a fresh write deadline.
func writeBridgeLine(rc *http.ResponseController, w io.Writer, line []byte) error {
	if err := rc.SetWriteDeadline(time.Now().Add(bridgeWriteTimeout)); err != nil {
		return fmt.Errorf("setting write deadline: %w", err)
	}
	if len(line) > 0 {
		if _, err := w.Write(line); err != nil {
			return fmt.Errorf("writing command: %w", err)
		}
	}
	if err := rc.Flush(); err != nil {
		return fmt.Errorf("flushing: %w", err)
	}
	return nil
}

// handleBridgeReport applies one ack, hello or event report from agent's
// mod. The body is validated strictly; anything off-contract is a 400.
func (s *Server) handleBridgeReport(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if !config.ValidName(agent) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid agent name %q", agent))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBridgeReportBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("report exceeds %d bytes", maxBridgeReportBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "reading report: "+err.Error())
		return
	}
	report, err := bridge.ParseReport(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bridge: %s: rejected report: %v\n", agent, err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.bridge.Apply(agent, report); err != nil {
		writeError(w, bridgeErrorStatus(err, http.StatusBadRequest), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// bridgeErrorStatus maps a hub error to its HTTP status: 410 Gone for a
// forgotten key (its launch is over; see bridge.Hub.Forget), 503 once the
// hub has closed, else fallback.
func bridgeErrorStatus(err error, fallback int) int {
	switch {
	case errors.Is(err, bridge.ErrForgotten):
		return http.StatusGone
	case errors.Is(err, bridge.ErrClosed):
		return http.StatusServiceUnavailable
	default:
		return fallback
	}
}
