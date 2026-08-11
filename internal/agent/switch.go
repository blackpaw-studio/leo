package agent

import (
	"fmt"
	"log"
	"maps"

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
	// Status is the agent's state, "running" or "suspended" — a suspended
	// agent is re-pointed in place and comes up on the new template at its
	// next resume.
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
// suspended agent is rewritten in place — there is no process to bounce, and
// its next resume comes up on the new template. Stopped agents, agents with no
// record, undefined templates, and agents backing a `runtime: persistent` task
// are all refused (see the guards below).
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
	switch {
	case live:
	case rec.Suspended:
		status = "suspended"
	default:
		return SwitchResult{}, fmt.Errorf("agent %q is not running (only running or suspended agents can switch template)", name)
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

	next := withTemplate(rec, template, normalizeHarness(cfg.TemplateHarness(tmpl)))
	// archived is what the arriving template left behind, before any minting
	// below — the difference between "rejoin that conversation" and "there
	// isn't one yet".
	archived := next.SessionID
	resumeID := archived
	isClaude := next.Harness == "claude"

	args, env, built := resolveTemplateWiring(cfg, next, tmpl, m.webToken)
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
	// SessionArgsRefresher reads the id off the record at launch, and an empty
	// id re-arms post-hoc discovery for a fresh conversation.
	next.ClaudeArgs = args
	next.Env = env

	if status == "suspended" {
		// Nothing to bounce, and no minted session id to keep: the agent will
		// not launch until Resume runs, and Resume rebuilds its args from the
		// record, so storing an id for a session nothing has created yet would
		// hand it a --resume for a conversation that does not exist. Fall back
		// to exactly what the arriving template had archived — which may be a
		// real session to rejoin, or nothing at all.
		next.SessionID = archived
		if isClaude {
			next.ClaudeArgs = ResumeArgs(args, archived)
		}
		if err := agentstore.Save(cfg.HomePath, next); err != nil {
			return SwitchResult{}, fmt.Errorf("saving switched agent record: %w", err)
		}
		return switchResult(rec, next, status), nil
	}

	if err := m.sup.StopAgent(name); err != nil {
		return SwitchResult{}, fmt.Errorf("stopping agent for template switch: %w", err)
	}
	// Persist before the respawn so a racing RestoreAgents brings the agent up
	// on the new template, not the departing one. SessionID stays empty until
	// the spawn succeeds: a minted --session-id names a session that does not
	// exist yet, and a restore that tried to --resume it would crash-loop.
	pending := next
	pending.SessionID = ""
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
		return SwitchResult{}, fmt.Errorf("respawning %q on template %q: %w (re-run the switch to retry)", name, template, err)
	}

	next.SessionID = resumeID
	if err := agentstore.Save(cfg.HomePath, next); err != nil {
		log.Printf("agent %q switched to template %q but agentstore.Save failed: %v — session id may lag until next save", name, template, err)
	}
	return switchResult(rec, next, status), nil
}

// withTemplate returns a copy of rec re-pointed at template/harness, with the
// session archive rotated: the departing template's live session id is filed
// away and the arriving template's is popped back out. rec is never mutated.
//
// SessionPinned is set so the next resume takes the restored id verbatim: the
// newest-transcript preference in Restart/Resume/RestoreAgents is workspace-wide
// and template-blind, so left alone it would resume the departing template's
// conversation and quietly undo the swap. NoResume is cleared because it
// describes a quick-exit on the template being left behind.
func withTemplate(rec agentstore.Record, template, harness string) agentstore.Record {
	archive := maps.Clone(rec.SessionsByTemplate)
	if archive == nil {
		archive = map[string]string{}
	}
	// An agent with no template (a from-agent spawn that inherited none) has
	// no key to file its session under; it is dropped rather than misfiled.
	if rec.Template != "" && rec.SessionID != "" {
		archive[rec.Template] = rec.SessionID
	}
	restored := archive[template]
	delete(archive, template)

	next := rec
	next.Template = template
	next.Harness = harness
	next.SessionID = restored
	next.SessionPinned = true
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
