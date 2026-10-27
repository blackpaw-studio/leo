package agentmove

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Commit phases a destination records for a transfer, which Status reports
// to a source whose commit answer was lost.
const (
	PhaseCommitting = "committing"
	PhaseHooks      = "hooks"
	PhaseStarting   = "starting"
	PhaseLive       = "live"
	// PhaseFailed is the only settled failure: the agent was left dormant
	// here (or a copy handed back to its partial), so the source may start
	// its own.
	PhaseFailed = "failed"
)

// commitState is the last phase a transfer's commit reached here.
type commitState struct {
	Transfer string    `json:"transfer"`
	Phase    string    `json:"phase"`
	At       time.Time `json:"at"`
}

func commitStatePath(homePath, name string) string {
	return filepath.Join(homePath, "state", "moves", name, "commit.json")
}

// recordPhase writes phase for m's transfer, replacing the file whole. A
// failed write only logs: the file then holds an earlier, unsettled phase
// (or none), which a source reads as an unknown outcome.
func recordPhase(homePath string, m Manifest, phase string, at time.Time) {
	if err := writeCommitState(commitStatePath(homePath, m.TargetName()), commitState{Transfer: m.Transfer, Phase: phase, At: at}); err != nil {
		log.Printf("[move:%s] recording commit phase %s: %v", m.TargetName(), phase, err)
	}
}

func writeCommitState(path string, st commitState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// commitPhase is the phase recorded here for m's transfer, "" when none is
// (another transfer's, or a manifest with no transfer id).
func commitPhase(homePath string, m Manifest) string {
	if m.Transfer == "" {
		return ""
	}
	data, err := os.ReadFile(commitStatePath(homePath, m.TargetName()))
	if err != nil {
		return ""
	}
	var st commitState
	if json.Unmarshal(data, &st) != nil || st.Transfer != m.Transfer {
		return ""
	}
	return st.Phase
}
