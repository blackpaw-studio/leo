package consult

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// readGrant is the cross-workspace read a run is under (target_agent): the
// target agent, the readable_by entry (or exempt grant) that admitted it, and
// the target workspace as configured and symlink-resolved. Inherited is true
// for a run that took it from its parent instead of being granted itself.
type readGrant struct {
	Agent      string
	Grant      string
	Workspaces []string
	Inherited  bool
}

func (g readGrant) active() bool { return g.Agent != "" }

// grantFromParent is the read grant a child of parent inherits, if any.
func grantFromParent(parent *Record) readGrant {
	if parent == nil || parent.TargetAgent == "" {
		return readGrant{}
	}
	return readGrant{Agent: parent.TargetAgent, Grant: parent.TargetGrant, Workspaces: slices.Clone(parent.TargetWorkspaces), Inherited: true}
}

// checkTargetRequest refuses the request shapes a read grant never admits,
// before any defaulting: target_agent is a role-routed, read-only dispatch
// whose cwd Leo chooses, and nothing under a granted tree may make a
// worktree (creating one writes into the target repository's .git).
func checkTargetRequest(req Request, inherited readGrant) error {
	if inherited.active() && req.Isolation != "" {
		return invalidf("isolation is not allowed under a read grant on agent %q", inherited.Agent)
	}
	if req.TargetAgent == "" {
		return nil
	}
	switch {
	case strings.ContainsAny(req.TargetAgent, ":@"):
		return invalidf("target_agent %q must be a local agent name (remote agents and tasks cannot be targeted)", req.TargetAgent)
	case inherited.active() && req.TargetAgent != inherited.Agent:
		return invalidf("target_agent %q: this run is under a read grant on agent %q and cannot target another agent", req.TargetAgent, inherited.Agent)
	case requestKind(req) != "dispatch":
		return invalidf("target_agent is only valid on a dispatch, not a %s", requestKind(req))
	case req.Cwd != "":
		return invalidf("target_agent and cwd are mutually exclusive: the run's cwd is the target's workspace")
	case req.Isolation != "":
		return invalidf("target_agent and isolation are mutually exclusive")
	case req.Role == "":
		return invalidf("target_agent needs a role (explore, plan or review*); a template dispatch cannot target an agent")
	case !config.IsReadOnlyRole(req.Role):
		return invalidf("target_agent needs a read-only role (explore, plan or review*), not %q", req.Role)
	}
	return nil
}

// checkSandboxHarness refuses a granted run on a harness that cannot enforce
// the read-only sandbox.
func checkSandboxHarness(grant readGrant, targetAgent string, h harness.Harness) error {
	if (grant.active() || targetAgent != "") && !harness.SupportsReadOnlySandbox(h) {
		return invalidf("target_agent: harness %q has no enforceable read-only sandbox", h.Name())
	}
	return nil
}

// resolveReadGrant evaluates req.TargetAgent against the target's template
// grants for the dispatch tree's root caller. It is evaluated in every
// confinement mode: target_agent is an explicit request, never a fallback.
// Exempt callers (none, api clients) are granted without an entry.
func (d *Dispatcher) resolveReadGrant(cfg *config.Config, req Request, rootCaller string) (readGrant, error) {
	d.mu.Lock()
	resolver := d.callerWorkspaces
	d.mu.Unlock()
	if resolver == nil {
		return readGrant{}, invalidf("target_agent %q is not a known local agent", req.TargetAgent)
	}
	home, ok := resolver.CallerWorkspace(cfg, req.TargetAgent)
	if !ok || home.Workspace == "" {
		return readGrant{}, invalidf("target_agent %q is not a known local agent", req.TargetAgent)
	}
	root := RootIdentity{Caller: rootCaller}
	resolved := false
	if rootCaller != "" {
		var rootHome CallerHome
		rootHome, resolved = resolver.CallerWorkspace(cfg, rootCaller)
		root.Template = rootHome.Template
	}
	_, isAPIClient := cfg.APIClients[rootCaller]
	readableBy := cfg.Templates[home.Template].Share.ReadableBy
	entry, ok := matchGrant(readableBy, root, isAPIClient && !resolved)
	if !ok {
		return readGrant{}, invalidf("target_agent %q: caller %q has no read grant; the target's template must list it in templates.%s.share.readable_by (agent:<name>, template:<name> or task:<name>)", req.TargetAgent, rootCaller, home.Template)
	}
	return readGrant{Agent: req.TargetAgent, Grant: entry, Workspaces: d.targetWorkspaces(home.Workspace)}, nil
}

// targetWorkspaces is the target workspace as configured and, when it
// differs, symlink-resolved: the sandbox must deny writes under both spellings.
func (d *Dispatcher) targetWorkspaces(ws string) []string {
	out := []string{ws}
	if real, err := d.confinementDeps().evalSymlinks(ws); err == nil && real != ws {
		out = append(out, real)
	}
	return out
}

// readOnlyFor is the launch sandbox a record's run needs: read-only in its
// target workspaces when it is (or descends from) a granted read.
func readOnlyFor(rec Record) *harness.ReadOnlySandbox {
	if rec.TargetAgent == "" {
		return nil
	}
	return &harness.ReadOnlySandbox{Workspaces: slices.Clone(rec.TargetWorkspaces)}
}

// codexPermissionWarning notes that a granted read overrides a codex
// template's permission_mode.
func codexPermissionWarning(cfg *config.Config, tmpl config.TemplateConfig, h harness.Harness) string {
	if h.Name() != "codex" {
		return ""
	}
	mode, _ := cfg.TemplateHarnessOptions(tmpl)["permission_mode"].(string)
	if mode == "" || mode == "read-only" {
		return ""
	}
	return fmt.Sprintf("permission_mode %q overridden: a read grant runs codex with --sandbox read-only", mode)
}

// logGrant writes the audit line for a granted (not inherited) start.
func (d *Dispatcher) logGrant(rec Record, rootCaller string) {
	d.logf("dispatch %s: read grant caller=%s target=%s role=%s grant=%s", rec.ID, rootCaller, rec.TargetAgent, rec.Role, rec.TargetGrant)
}

// logf writes one daemon log line through Logf, or to stderr without one.
func (d *Dispatcher) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
