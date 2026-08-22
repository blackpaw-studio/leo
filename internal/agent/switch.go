package agent

import (
	"fmt"
	"log"
	"maps"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/session"
)

// SwitchResult describes what a template switch did, so callers can report the
// two things a user cares about: which blueprint the agent now runs, and what
// happened to the conversation.
type SwitchResult struct {
	Name         string `json:"name"`
	FromTemplate string `json:"from_template"`
	ToTemplate   string `json:"to_template"`
	FromHarness  string `json:"from_harness"`
	ToHarness    string `json:"to_harness"`
	// Resumed is true when the target template had an archived session that
	// was handed back, false when it starts a fresh conversation.
	Resumed bool `json:"resumed"`
	// Status is the agent's state after the switch: "running" for a live
	// agent that was stopped and respawned on the new template, "stopped"
	// for a dormant agent whose record was rewritten in place with no
	// respawn.
	Status string `json:"status"`
	// Unchanged marks a no-op: the agent was already on the target template,
	// so nothing was stopped, respawned, or archived.
	Unchanged bool `json:"unchanged,omitempty"`
}

// normalizeHarness maps the empty harness to "claude". Records and templates
// written before the harness field existed leave it unset, and every read site
// has to treat that as claude.
func normalizeHarness(h string) string {
	if h == "" {
		return "claude"
	}
	return h
}

// SwitchTemplate re-points an existing agent at a different template, keeping
// its name, workspace, and worktree, and swapping which conversation is live.
//
// Sessions are per-template. Switching A→B archives A's session id under A in
// the record (SessionsByTemplate) and pops B's back out, so returning to a
// template resumes the conversation it left behind; a template being visited
// for the first time starts fresh. Because the archive is keyed by template
// rather than harness, two templates on the same harness keep separate
// conversations.
//
// A running agent is stopped and respawned on the new template's wiring. A
// dormant (stopped) agent is rewritten in place — there is no process to
// bounce, and it comes up on the new template the next time it is started.
// Every dormant record is fully intact (Stop always keeps the record and its
// SessionID now, whatever the workspace type), so there is nothing left that
// makes rewriting one unsafe. Agents with no record, undefined templates, and
// agents backing a `runtime: persistent` task are all refused too (see the
// guards below).
//
// The record is saved before the respawn so a daemon restart racing this call
// sees the new template rather than the old one, which does mean a failed
// respawn leaves the agent down on the new template; re-running the switch (or
// `leo agent restart`) recovers, exactly as it does for Reset.
func (m *Manager) SwitchTemplate(name, template string) (SwitchResult, error) {
	cfg, err := m.cfgLoader()
	if err != nil {
		return SwitchResult{}, fmt.Errorf("loading config: %w", err)
	}
	tmpl, ok := cfg.Templates[template]
	if !ok {
		return SwitchResult{}, fmt.Errorf("no template %q in config", template)
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return SwitchResult{}, fmt.Errorf("loading agentstore: %w", err)
	}
	rec, ok := stored[name]
	if !ok {
		return SwitchResult{}, fmt.Errorf("no agentstore record for %q (cannot switch the template of an unpersisted agent)", name)
	}

	_, live := m.sup.EphemeralAgents()[name]
	status := "running"
	if !live {
		// Not live and not dormant either: a crashed-without-cleanup record
		// this manager has no lifecycle verb for yet. Refuse rather than
		// silently rewriting a record no supervisor is tracking.
		if !rec.Stopped {
			return SwitchResult{}, fmt.Errorf("agent %q is neither running nor stopped (its record may be stale)", name)
		}
		status = "stopped"
	}

	if rec.Template == template {
		return SwitchResult{
			Name: name, FromTemplate: rec.Template, ToTemplate: template,
			FromHarness: normalizeHarness(rec.Harness), ToHarness: normalizeHarness(rec.Harness),
			Status: status, Unchanged: true,
		}, nil
	}
	if task, bound := persistentTaskFor(cfg, name); bound {
		return SwitchResult{}, fmt.Errorf(
			"agent %q backs persistent task %q, which binds to it by name — change tasks.%s.template in config instead of switching the agent",
			name, task, task)
	}

	// resumeIDFor, not rec.SessionID: a /clear starts a session the store never
	// saw, and the archive has to file away the conversation the agent is
	// actually in or switching back resurrects a dead thread.
	// resumeIDFor, not rec.SessionID: a /clear starts a session the store never
	// saw, and the archive has to file away the conversation the agent is
	// actually in or switching back resurrects a dead thread.
	next := withTemplate(rec, template, normalizeHarness(cfg.TemplateHarness(tmpl)), ResumeIDFor(rec))
	// archived is what the arriving template left behind — the difference
	// between "rejoin that conversation" and "there isn't one yet".
	archived := next.SessionID
	resumeID := archived
	isClaude := next.Harness == "claude"

	// Idle-suspend cascades from the arriving template like the rest of the
	// wiring; the stored interval describes the one being left. A per-spawn
	// --idle-suspend override cannot be told apart from a resolved value on
	// the record, so it does not survive a switch — the same trade the env
	// rebuild makes.
	next.IdleSuspendAfter = ""
	if d := cfg.ResolveIdleSuspend(tmpl, ""); d > 0 {
		next.IdleSuspendAfter = d.String()
	}

	// Resolve the new wiring BEFORE stopping anything: a template that cannot
	// produce launch args must fail the switch with the agent still running,
	// not leave it dead on a template it never reached.
	args, env, built := resolveTemplateWiring(cfg, next, tmpl, m.webToken, rebuildEnvFromTemplate)
	if !built {
		return SwitchResult{}, fmt.Errorf("building %s wiring for template %q failed (agent left on %q; see the daemon log)", next.Harness, template, rec.Template)
	}
	if isClaude {
		if resumeID != "" {
			args = ResumeArgs(args, resumeID)
		} else {
			// First visit to this claude template: mint a session id the same
			// way a fresh spawn does, so leo knows the conversation it just
			// started instead of having to rediscover it from disk.
			resumeID = session.NewID()
			args = append(args, "--session-id", resumeID)
		}
	}
	// Non-claude harnesses take no session flag here: their driver's
	// SessionArgsRefresher reads the id off the RECORD at launch, and an empty
	// id re-arms post-hoc discovery for a fresh conversation.
	next.ClaudeArgs = args
	next.Env = env

	if status == "stopped" {
		// Nothing to bounce, and no minted session id to keep: the agent will
		// not launch until Start runs, and Start rebuilds its args from the
		// record, so storing an id for a session nothing has created yet would
		// hand it a --resume for a conversation that does not exist. Fall back
		// to exactly what the arriving template had archived — which may be a
		// real session to rejoin, or nothing at all. Stopped and WakeOnMessage
		// carry over unchanged: the switch is a pure rewrite, not a dormancy
		// transition.
		next.SessionID = archived
		if isClaude {
			next.ClaudeArgs = ResumeArgs(args, archived)
		}
		next.SessionPinnedAt = pin(time.Now())
		next.Stopped = rec.Stopped
		next.WakeOnMessage = rec.WakeOnMessage
		if err := agentstore.Save(cfg.HomePath, next); err != nil {
			return SwitchResult{}, fmt.Errorf("saving switched agent record: %w", err)
		}
		return switchResult(rec, next, status), nil
	}

	if err := m.sup.StopAgent(name); err != nil {
		return SwitchResult{}, fmt.Errorf("stopping agent for template switch: %w", err)
	}
	// Stamped after the stop, not before: the departing process can still be
	// flushing its transcript on the way down, and a pin predating that write
	// would let ResumeIDFor prefer the conversation just switched away from.
	next.SessionPinnedAt = pin(time.Now())

	// Persist before the respawn so a racing RestoreAgents brings the agent up
	// on the new template, not the departing one — and with the ARCHIVED id,
	// because a non-claude harness's driver reads the session to resume off
	// this record at launch. A minted claude id is held back until the spawn
	// succeeds: it names a session that does not exist yet, and a restore that
	// tried to --resume it would crash-loop.
	pending := next
	pending.SessionID = archived
	if err := agentstore.Save(cfg.HomePath, pending); err != nil {
		return SwitchResult{}, fmt.Errorf("saving switched agent record: %w", err)
	}

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       next.Name,
		ClaudeArgs: next.ClaudeArgs,
		WorkDir:    next.Workspace,
		Env:        next.Env,
		WebPort:    next.WebPort,
		WebToken:   m.webToken,
		Harness:    next.Harness,
	}); err != nil {
		// The agent is stopped and the arriving template's session has already
		// been popped out of the archive, so it survives only on the record.
		// Mark it dormant (not wakeable): that is the one state a down agent
		// can come back from with its conversation (Start), and re-running
		// the switch could not, since it refuses an agent that is neither
		// running nor stopped.
		down := next
		down.SessionID = archived
		down.Stopped = true
		down.WakeOnMessage = false
		if saveErr := agentstore.Save(cfg.HomePath, down); saveErr != nil {
			log.Printf("agent %q: could not mark stopped after a failed switch: %v", name, saveErr)
		}
		return SwitchResult{}, fmt.Errorf("respawning %q on template %q: %w (the agent is stopped on %s — 'leo agent start %s' brings it back)", name, template, err, template, name)
	}

	next.SessionID = resumeID
	if err := agentstore.Save(cfg.HomePath, next); err != nil {
		log.Printf("agent %q switched to template %q but agentstore.Save failed: %v — session id may lag until next save", name, template, err)
	}
	return switchResult(rec, next, status), nil
}

// pin returns a pointer to t for agentstore.Record.SessionPinnedAt, whose nil
// means "never switched".
func pin(t time.Time) *time.Time { return &t }

// withTemplate returns a copy of rec re-pointed at template/harness, with the
// session archive rotated: departingSession is filed under the template being
// left and the arriving template's id is popped back out. rec is never mutated.
//
// The caller stamps SessionPinnedAt afterwards — after stopping a running
// agent, so the pin postdates any transcript the departing process flushes on
// its way down. NoResume is cleared because it describes a quick-exit on the
// template being left behind.
func withTemplate(rec agentstore.Record, template, harness, departingSession string) agentstore.Record {
	archive := maps.Clone(rec.SessionsByTemplate)
	if archive == nil {
		archive = map[string]string{}
	}
	// An agent with no template (a from-agent spawn that inherited none) has
	// no key to file its session under; it is dropped rather than misfiled.
	if rec.Template != "" && departingSession != "" {
		archive[rec.Template] = departingSession
	}
	restored := archive[template]
	delete(archive, template)

	next := rec
	next.Template = template
	next.Harness = harness
	next.SessionID = restored
	next.NoResume = false
	next.SessionsByTemplate = archive
	if len(archive) == 0 {
		next.SessionsByTemplate = nil
	}
	return next
}

func switchResult(from, to agentstore.Record, status string) SwitchResult {
	return SwitchResult{
		Name:         to.Name,
		FromTemplate: from.Template,
		ToTemplate:   to.Template,
		FromHarness:  normalizeHarness(from.Harness),
		ToHarness:    normalizeHarness(to.Harness),
		Resumed:      to.SessionID != "" && to.SessionID == from.SessionsByTemplate[to.Template],
		Status:       status,
	}
}

// persistentTaskFor reports the first `runtime: persistent` task whose target
// agent is name. Those tasks bind to their agent by name (config.ResolveTaskTarget),
// so switching that agent's template would silently redirect a scheduled task's
// prompts into a template it was never configured for — including, when the
// harness changes, one that cannot deliver its channels at all.
func persistentTaskFor(cfg *config.Config, name string) (string, bool) {
	for taskName, task := range cfg.Tasks {
		if task.Runtime != "persistent" {
			continue
		}
		target, _, _, err := cfg.ResolveTaskTarget(taskName)
		if err != nil {
			continue
		}
		if target == name {
			return taskName, true
		}
	}
	return "", false
}
