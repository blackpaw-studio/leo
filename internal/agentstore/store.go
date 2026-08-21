// Package agentstore persists ephemeral agent records to disk.
// It is intentionally dependency-free (no imports from daemon, service, or web)
// to avoid import cycles.
package agentstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// storeMu serializes Load/Save/Remove across goroutines so concurrent daemon
// handlers (spawn, stop, prune) don't perform interleaved read-modify-write
// cycles that clobber each other's changes to agents.json.
var storeMu sync.Mutex

// Record persists an ephemeral agent so it can be restored after daemon restart.
// Branch and CanonicalPath are set iff the agent was spawned with --worktree;
// when Branch is empty the agent uses the Workspace directly as claude's cwd.
//
// SessionID is the claude session ID captured at spawn time. On daemon restart
// RestoreAgents rewrites the agent's claude args to pass `--resume <SessionID>`
// so conversation context is preserved across restarts.
//
// Stopped is set by Manager.Stop for worktree agents — the record is kept so
// `leo agent prune` can find the checkout, but RestoreAgents skips records
// marked Stopped so a user-stopped agent is not resurrected on daemon restart.
// Shared-workspace agents delete the record on stop, so Stopped only applies
// to worktree agents in practice.
type Record struct {
	Name          string            `json:"name"`
	Template      string            `json:"template"`
	Repo          string            `json:"repo,omitempty"`
	Workspace     string            `json:"workspace"`
	Branch        string            `json:"branch,omitempty"`
	CanonicalPath string            `json:"canonical_path,omitempty"`
	ClaudeArgs    []string          `json:"claude_args"`
	SessionID     string            `json:"session_id,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	WebPort       string            `json:"web_port"`
	SpawnedAt     time.Time         `json:"spawned_at"`
	Stopped       bool              `json:"stopped,omitempty"`

	// StoppedReason records WHY the system (not the user) marked this record
	// Stopped — e.g. a missing shared-workspace directory or a SpawnAgent
	// failure encountered while restoring agents at daemon boot. Empty when
	// Stopped was set by a user-initiated `leo agent stop` (or is unset).
	// Manager.List uses a non-empty StoppedReason to decide whether a
	// shared-workspace record stopped by a failed restore should still be
	// visible ("stopped") instead of dropping out of the list the way a
	// deliberately-stopped shared agent does (Stop deletes those records
	// outright, so they never reach List's stopped branch at all). Restart's
	// store fallback keys off the same field to know a record is recoverable.
	StoppedReason string `json:"stopped_reason,omitempty"`

	// Suspended marks an agent that the daemon idle-suspended: its process and
	// tmux session were killed to free resources, but the record (and
	// SessionID) is preserved so the conversation auto-resumes on the next
	// incoming message. Distinct from Stopped (user-initiated, terminal):
	// RestoreAgents skips Suspended records (no boot-time respawn) and Prune
	// keys off Stopped, so suspended worktrees are never pruned.
	Suspended bool `json:"suspended,omitempty"`

	// IdleSuspendAfter is the resolved idle interval (a Go duration string)
	// stamped at spawn time from the config cascade. The idle sweep reads this
	// off the record rather than re-resolving config, so behavior is stable
	// across config edits and daemon restarts. Empty means idle-suspend is off.
	IdleSuspendAfter string `json:"idle_suspend_after,omitempty"`

	// NoResume marks the next spawn as "do not pass --resume". Set by the
	// supervisor when a previous spawn quick-exited while resuming, to break
	// the crash loop across daemon restarts (the in-process strip is not
	// enough — restart-time RestoreAgents would otherwise pick the same
	// poisoned jsonl back up via LatestSession). Cleared by RestoreAgents
	// after it consumes the flag, so a subsequent healthy session is
	// resume-able again.
	NoResume bool `json:"no_resume,omitempty"`

	// Harness is the resolved harness adapter name this agent was spawned
	// with (e.g. "claude", "codex"). Empty means "claude" — records written
	// before this field existed predate it and must be treated as claude
	// everywhere it's read.
	Harness string `json:"harness,omitempty"`

	// SpawnEnv is exactly SpawnSpec.Env — the caller's explicit --env
	// overrides. It always wins on collision, including over harness/template
	// env, matching spawn-time layering. Restart re-resolves ClaudeArgs/Env
	// from current config when possible; SpawnEnv lets it rebuild Env as
	// mergeEnv(mergeEnv(mergeEnv(newHarnessEnv, tmpl.Env), prunedInherited),
	// rec.SpawnEnv) without clobbering caller-supplied overrides. Nil for
	// records written before this field existed (a legacy record with a nil
	// SpawnEnv AND a nil InheritedEnv keeps every stored Env key on restart
	// except those the current harness env owns — env that can't be
	// reconstructed is layered over a fresh harness env rather than dropped,
	// so harness-owned keys stay current while caller-supplied ones survive).
	SpawnEnv map[string]string `json:"spawn_env,omitempty"`

	// SessionsByTemplate archives the session id of every template this agent
	// has been away from, keyed by template name. It is an archive of INACTIVE
	// templates only — the active template's session always lives in
	// SessionID, so every existing resume path (Restart, Resume,
	// RestoreAgents, the drivers' SessionIDStore) is unaffected by it.
	// Maintained by Manager.SwitchTemplate: switching A→B stores A's SessionID
	// here and pops B's back out, so returning to a template resumes the
	// conversation it left behind. Keyed by template rather than harness
	// because two templates on the same harness (a coding and a review claude
	// template, say) are meant to keep separate conversations. Nil for records
	// that have never switched.
	SessionsByTemplate map[string]string `json:"sessions_by_template,omitempty"`

	// SessionPinnedAt is when a template switch handed this record its current
	// SessionID, and nil on a record that has not switched.
	//
	// Restart/Resume/RestoreAgents normally prefer the most recently modified
	// transcript in the workspace over the stored SessionID, to catch a /clear
	// session the store never saw. That scan is workspace-wide and
	// template-blind, so right after a switch it would resume the PREVIOUS
	// template's conversation and defeat SessionsByTemplate. The pin suppresses
	// it — but only for transcripts that predate the switch: anything written
	// since belongs to the template the agent is on now, so a /clear an hour
	// later still wins. Without that bound the pin would sit set until the next
	// restart, however many days away, and silently abandon the newer session
	// when it finally fired. See agent.ResumeIDFor.
	SessionPinnedAt *time.Time `json:"session_pinned_at,omitempty"`

	// InheritedEnv is the worktree/from-agent spawn's inherited env layer —
	// e.g. a spawnFromAgent's source-agent env — stored RAW, before the
	// spawn-time pruning against that spawn's harness env. Restart re-prunes
	// it against the CURRENT harness env (see agent.pruneEnv) rather than
	// replaying the spawn-time snapshot, so a harness env key that didn't
	// exist yet at spawn time still wins on restart instead of being shadowed
	// by a stale inherited value. Empty/nil for shared spawns (no inheritance
	// concept) and legacy records written before this field existed.
	InheritedEnv map[string]string `json:"inherited_env,omitempty"`
}

// IsFailedRestore reports whether this record was stopped by the system after
// a failed restore, not by the user: StoppedReason non-empty means
// system-marked (auto-retried at boot, recoverable via restart, removable via
// stop); empty means user-stopped. Stopped alone is not enough — a
// user-initiated `leo agent stop` also sets Stopped, but leaves StoppedReason
// empty.
func (r Record) IsFailedRestore() bool {
	return r.Stopped && r.StoppedReason != ""
}

// FilePath returns the path to agents.json in the state directory.
func FilePath(homePath string) string {
	return filepath.Join(homePath, "state", "agents.json")
}

// Save persists an agent record to agents.json.
func Save(homePath string, record Record) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	records[record.Name] = record
	return write(path, records)
}

// Remove deletes an agent record from agents.json.
func Remove(homePath, name string) {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	delete(records, name)
	_ = write(path, records)
}

// Load reads all agent records from disk.
func Load(path string) (map[string]Record, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	return loadLocked(path)
}

// loadLocked performs the read without acquiring storeMu. Callers that already
// hold the lock (Save, Remove) use this to avoid a re-entrant lock acquisition.
func loadLocked(path string) (map[string]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]Record), err
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return make(map[string]Record), err
	}
	return records, nil
}

// Rename atomically re-keys an agent record from old to new, applying mutate to
// the record before it is stored under the new key. It errors if old is absent
// or new already exists. The whole load-modify-write happens under storeMu so it
// is consistent with concurrent Save/Remove/Load.
func Rename(homePath, oldName, newName string, mutate func(Record) Record) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	rec, ok := records[oldName]
	if !ok {
		return fmt.Errorf("agent %q not found", oldName)
	}
	if _, exists := records[newName]; exists {
		return fmt.Errorf("agent %q already exists", newName)
	}
	records[newName] = mutate(rec)
	delete(records, oldName)
	return write(path, records)
}

// Update atomically re-reads, mutates, and re-saves a single agent record. It
// errors if name is absent. The whole load-modify-write happens under storeMu
// so it is consistent with concurrent Save/Remove/Load/Rename, matching the
// existing Rename helper's shape.
func Update(homePath, name string, mutate func(Record) Record) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	rec, ok := records[name]
	if !ok {
		return fmt.Errorf("agent %q not found", name)
	}
	records[name] = mutate(rec)
	return write(path, records)
}

func write(path string, records map[string]Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent records: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	// os.WriteFile's perm argument is only applied when the file is created;
	// an already-existing agents.json (e.g. written before this hardening, or
	// by any other looser-permission path) keeps its prior mode across the
	// write above. Records now persist OPENCODE_CONFIG_CONTENT, which can
	// embed LEO_API_TOKEN, so explicitly enforce 0600 on every save rather
	// than trusting create-time permissions.
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("enforcing agents.json permissions: %w", err)
	}
	return nil
}
