package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// copyStartFailed prefixes a copy's StoppedReason when its first launch
// fails; `leo agent start` recovers it.
const copyStartFailed = "copy start failed"

// ReceiveSpec is an agent arriving on this host: moved here, or a copy.
type ReceiveSpec struct {
	// Record is the source record under its name and workspace here (a
	// copy's new identity applied). Only its portable fields are kept.
	Record agentstore.Record
	// From is the source's hostname: the move-log key, and where a shadow
	// points when the start fails.
	From string
	At   time.Time
	// Copy marks a fork: no move log, no archived sessions, and the first
	// launch forks ForkFrom into Record.SessionID (or into an id the
	// harness discovers when that is empty).
	Copy     bool
	ForkFrom string
}

// Receive saves an arriving agent and starts it. Launch args and env are
// rebuilt from this host's config; the conversation is pinned so the sent
// session wins over any older transcript in the workspace. A move may land
// on its own moved shadow (a move back); any other existing record refuses.
// When the start fails the record is left dormant (ShadowReceive).
func (m *Manager) Receive(_ context.Context, spec ReceiveSpec) error {
	cfg, err := m.cfgLoader()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	stored, err := loadStore(cfg.HomePath)
	if err != nil {
		return err
	}
	name := spec.Record.Name
	if existing, ok := stored[name]; ok && (spec.Copy || !existing.Moved()) {
		return fmt.Errorf("receiving %q: an agent with that name already exists here", name)
	}
	rec := arrivedRecord(spec)
	tmpl, ok := templateOf(cfg, rec)
	if !ok {
		return &UnknownTemplateError{Name: rec.Template}
	}
	args, env, err := resolveTemplateWiring(cfg, rec, tmpl, m.webToken, m.leoMCP, rebuildEnvFromTemplate, m.env)
	if err != nil {
		return fmt.Errorf("receiving %q: %w", name, err)
	}
	rec.ClaudeArgs, rec.Env = args, env
	rec.WebPort = fmt.Sprint(cfg.WebPort())
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		return fmt.Errorf("saving %q: %w", name, err)
	}
	// Save keeps the stored appearance; the arriving one replaces it.
	if err := agentstore.Update(cfg.HomePath, name, func(r agentstore.Record) agentstore.Record {
		r.Appearance = rec.Appearance
		return r
	}); err != nil {
		return fmt.Errorf("saving %q: %w", name, err)
	}
	if err := m.Start(name); err != nil {
		if serr := m.ShadowReceive(spec, err); serr != nil {
			log.Printf("[agent:%s] leaving the failed receive dormant: %v", name, serr)
		}
		return err
	}
	return nil
}

// loadStore reads agents.json; a host with no agents yet has none.
func loadStore(homePath string) (map[string]agentstore.Record, error) {
	stored, err := agentstore.Load(agentstore.FilePath(homePath))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]agentstore.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading agentstore: %w", err)
	}
	return stored, nil
}

// arrivedRecord is spec's record as this host stores it before the start.
func arrivedRecord(spec ReceiveSpec) agentstore.Record {
	rec := spec.Record.Portable()
	at := spec.At
	rec.SpawnedAt = at
	rec.Stopped = true
	rec.SessionPinnedAt = &at
	if spec.Copy {
		// A copy is a new agent: it shares no history with its source's
		// other conversations or moves.
		rec.MoveLog = nil
		rec.SessionsByTemplate = nil
		rec.ForkFromSession = spec.ForkFrom
	} else {
		rec.MoveLog = withMoveLog(rec.MoveLog, spec.From, at)
	}
	return rec
}

// ShadowReceive leaves a receive that failed after its record could exist
// dormant: a move's record becomes a shadow pointing back at the source
// (Start refuses it; the source's next move lands on it), a copy's a
// stopped agent `leo agent start` recovers. A live process is stopped
// first. A move with no record yet gets its shadow created.
func (m *Manager) ShadowReceive(spec ReceiveSpec, cause error) error {
	cfg, err := m.cfgLoader()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	name := spec.Record.Name
	if _, live := m.sup.EphemeralAgents()[name]; live {
		if err := m.sup.StopAgent(name, false); err != nil {
			return fmt.Errorf("stopping %q: %w", name, err)
		}
	}
	stored, err := loadStore(cfg.HomePath)
	if err != nil {
		return err
	}
	rec, ok := stored[name]
	switch {
	case !ok && spec.Copy:
		return nil
	case !ok:
		rec = arrivedRecord(spec)
	}
	at := spec.At
	rec.Stopped = true
	rec.WakeOnMessage = false
	if spec.Copy {
		rec.StoppedReason = fmt.Sprintf("%s: %v", copyStartFailed, cause)
	} else {
		rec.MovedTo = spec.From
		rec.MovedAt = &at
	}
	return agentstore.Save(cfg.HomePath, rec)
}

// forkArgs is a copy's first launch: args resuming rec.ForkFromSession as a
// new session (rec.SessionID when the harness pins one).
func forkArgs(rec agentstore.Record, args []string) ([]string, error) {
	h, err := harness.Get(normalizeHarness(rec.Harness))
	if err != nil {
		return nil, err
	}
	f, ok := h.(harness.Forker)
	if !ok {
		return nil, fmt.Errorf("harness %s cannot fork a session", h.Name())
	}
	return f.ForkArgs(args, rec.ForkFromSession, rec.SessionID), nil
}
