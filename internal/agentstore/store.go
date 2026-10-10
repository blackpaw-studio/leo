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
// Stopped is the single dormant flag: Manager.Stop always keeps the record
// (and SessionID) regardless of workspace type, and RestoreAgents skips
// records marked Stopped unless IsFailedRestore reports the system (not the
// user) put them there. Deletion is the only thing that removes a record —
// see Manager.Delete.
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

	// WakeOnMessage carries the INTENT behind a Stopped=true record, not a
	// separate state: true means an inbound message (a routed channel message
	// or a persistent task's ensure-exists delivery) is allowed to auto-start
	// this agent again; false means it stays dormant until an operator runs
	// Start explicitly. The idle sweep stops agents with WakeOnMessage=true;
	// an operator-initiated stop sets it false. Start clears both Stopped and
	// this flag. Meaningless (and always false) when Stopped is false.
	WakeOnMessage bool `json:"wake_on_message,omitempty"`

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

	// Environments is the per-agent override of the named-environment list
	// (a spawn's --environment or set-environment), by NAME so config edits
	// take effect on the next restart. Nil means the template's (or
	// defaults') list applies.
	Environments []string `json:"environments,omitempty"`

	// EnvLayered marks a record whose Env was composed from layers
	// (harness < named environments < template env < inherited < SpawnEnv),
	// so restart can rebuild every layer from current config instead of
	// replaying the stored blob. False on records that predate named
	// environments, whose env keys leo cannot attribute to a layer.
	EnvLayered bool `json:"env_layered,omitempty"`

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

	// AttentionToken is the per-launch token the agent's current session
	// reports attention hooks with; empty means it launched without hooks.
	// The supervisor re-registers it when adopting the session after a
	// daemon restart. SetAttentionToken is its only writer: Save always
	// keeps the stored value, so a load-modify-Save that read the record
	// before the launch can never wipe it.
	AttentionToken string `json:"attention_token,omitempty"`

	// OpeningBriefID identifies the private file a claude ephemeral agent's
	// initial opening prompt was written to at spawn time (see
	// claudeharness.AgentBriefPathForID), or "" when the agent was spawned
	// with no opening prompt or on a non-claude harness. It is exactly 32 hex
	// characters — a random id, NOT derived from the agent name, so a rename
	// followed by a fresh spawn under the freed name can never collide with
	// (and overwrite) an old brief. It is never embedded in ClaudeArgs as
	// literal argv text: the supervisor derives the path from this id,
	// Lstat-verifies it (regular file, mode 0600, owned by the current uid,
	// never a symlink — see claudeharness.VerifyAgentBriefFile), and only
	// then appends the $(cat <path>) substitution itself, after every
	// ClaudeArgs element has already been shell-quoted — so nothing in
	// ClaudeArgs (whether hand-authored config or a persisted record) can
	// ever smuggle an unquoted shell word into the launch command.
	//
	// Start/Restart both rebuild ClaudeArgs fresh with no opening prompt and
	// clear this field to match (a restart/resume never re-sends the opening
	// prompt — see agent.resolveOpeningPrompt). Reset instead replays the
	// stored ClaudeArgs verbatim and carries this field forward unchanged, so
	// resetting an agent re-sends its original opening prompt, exactly like
	// resetting used to replay the prompt baked into ClaudeArgs before this
	// field existed. The file itself is removed only on Delete — not on Stop
	// or Rename (which needs no file handling at all now: the id travels with
	// the record regardless of name) — so a stop-then-reset never loses the
	// prompt; SweepOpeningPromptBriefs cleans up files a restart has since
	// orphaned.
	OpeningBriefID string `json:"opening_brief_id,omitempty"`

	// OpeningAckedID is bridge.OpeningID(conversation, brief) for the
	// conversation that last received this agent's opening prompt: the
	// session the leo bridge mod acked it in, or the one a legacy launch
	// carried it into on argv. "" while none has. A launch that resumes that
	// conversation does not get the opening again; any other conversation,
	// and every launch that starts a fresh one, still does. SetOpeningAcked
	// is its only writer: Save always keeps the stored value (see
	// AttentionToken).
	OpeningAckedID string `json:"opening_acked_id,omitempty"`

	// OpeningQueuedLaunch and OpeningQueuedID name the opening the agent's
	// latest bridged launch queued and its mod has not acked yet: the
	// launch token (LEO_BRIDGE_LAUNCH) it went to and its command id. A
	// restarted daemon adopting that launch's surviving session queues it
	// again under the same id, so the mod's dedup turns a repeat into a
	// re-ack. SetOpeningQueued and SetOpeningAcked are their only writers:
	// Save always keeps the stored values.
	OpeningQueuedLaunch string `json:"opening_queued_launch,omitempty"`
	OpeningQueuedID     string `json:"opening_queued_id,omitempty"`
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

// Save persists an agent record to agents.json. The stored AttentionToken
// and opening delivery state are kept whatever record carries (see their
// fields).
func Save(homePath string, record Record) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	stored := records[record.Name]
	record.AttentionToken = stored.AttentionToken
	record.OpeningAckedID = stored.OpeningAckedID
	record.OpeningQueuedLaunch, record.OpeningQueuedID = stored.OpeningQueuedLaunch, stored.OpeningQueuedID
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

// legacyProbe decodes only the fields a pre-migration record might carry, so
// loadLocked can detect them without Record itself keeping dead fields around.
type legacyProbe struct {
	Suspended bool `json:"suspended"`
}

// loadLocked performs the read without acquiring storeMu. Callers that already
// hold the lock (Save, Remove) use this to avoid a re-entrant lock acquisition.
//
// One-way migration: a record written by a pre-one-dormant-state binary may
// carry `suspended: true`. Record has no Suspended field to unmarshal it into,
// so it's recovered via a second decode into legacyProbe and translated to
// `Stopped: true, WakeOnMessage: true` — a suspended agent always allowed
// auto-wake on the next message, matching Suspend's old behavior. A record
// with `stopped: true` and no `suspended` key is left with WakeOnMessage
// false, its zero value. The `suspended` key itself is silently dropped the
// next time this record is saved, since Record no longer has a field for it.
func loadLocked(path string) (map[string]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]Record), err
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return make(map[string]Record), err
	}
	var probes map[string]legacyProbe
	if err := json.Unmarshal(data, &probes); err == nil {
		for name, probe := range probes {
			if !probe.Suspended {
				continue
			}
			rec := records[name]
			rec.Stopped = true
			rec.WakeOnMessage = true
			records[name] = rec
		}
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

// SetAttentionToken records the attention token of name's current launch
// ("" for an unhooked launch). It errors if name is absent.
func SetAttentionToken(homePath, name, token string) error {
	return Update(homePath, name, func(r Record) Record {
		r.AttentionToken = token
		return r
	})
}

// SetOpeningAcked records that name's opening prompt reached a conversation
// (id is its bridge.OpeningID), and, when launch is the one the queued
// opening went to, that it is no longer queued. A legacy delivery passes an
// empty launch. It errors if name is absent.
func SetOpeningAcked(homePath, name, id, launch string) error {
	return Update(homePath, name, func(r Record) Record {
		r.OpeningAckedID = id
		if launch != "" && r.OpeningQueuedLaunch == launch {
			r.OpeningQueuedLaunch, r.OpeningQueuedID = "", ""
		}
		return r
	})
}

// SetOpeningQueued records that launch, about to start, has name's opening
// queued under id. It errors if name is absent.
func SetOpeningQueued(homePath, name, launch, id string) error {
	return Update(homePath, name, func(r Record) Record {
		r.OpeningQueuedLaunch, r.OpeningQueuedID = launch, id
		return r
	})
}

// ClearAttentionToken clears name's attention token only while it is still
// token, so a launch that ended never wipes a newer launch's token. A missing
// record is not an error.
func ClearAttentionToken(homePath, name, token string) error {
	if token == "" {
		return nil
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	path := FilePath(homePath)
	records, _ := loadLocked(path)
	rec, ok := records[name]
	if !ok || rec.AttentionToken != token {
		return nil
	}
	rec.AttentionToken = ""
	records[name] = rec
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
