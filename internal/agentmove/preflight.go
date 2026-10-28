package agentmove

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/git"
)

// CheckDestination is the destination's verdict on a manifest: every reason
// the transfer cannot land here, none when it can. It is pure; the
// Receiver gathers the facts.
func CheckDestination(m Manifest, d DestFacts) []Problem {
	var ps []Problem
	add := func(p *Problem) {
		if p != nil {
			ps = append(ps, *p)
		}
	}
	if m.Protocol != Protocol {
		add(problemf(CodeProtocolMismatch, "the source speaks move protocol %d, %s speaks %d; run the same leo version on both", m.Protocol, d.Host.Hostname, Protocol))
	}
	if m.From.Home != d.Host.Home {
		add(problemf(CodeHomeMismatch, "home is %s on %s but %s here; moves need identical paths", m.From.Home, m.From.Hostname, d.Host.Home))
	}
	if m.From.User != d.Host.User {
		add(problemf(CodeUserMismatch, "the source runs as %s, %s as %s", m.From.User, d.Host.Hostname, d.Host.User))
	}
	add(checkAgentConfig(m, d))
	add(checkHarness(m, d))
	add(checkIdentity(m, d))
	for _, p := range []string{m.TargetWorkspace(), d.SessionRoot, d.StagingDir} {
		if p != "" {
			add(asProblem(ValidatePath(p)))
		}
	}
	if m.gitMode() {
		ps = append(ps, checkDestGit(m, d)...)
	}
	return ps
}

func checkAgentConfig(m Manifest, d DestFacts) *Problem {
	if m.Agent.Template != "" && !d.TemplateFound {
		if d.TemplateInvalid != "" {
			return problemf(CodeTemplateMissing, "template %q is invalid on %s: %s", m.Agent.Template, d.Host.Hostname, d.TemplateInvalid)
		}
		return problemf(CodeTemplateMissing, "template %q is not defined on %s; add it there first", m.Agent.Template, d.Host.Hostname)
	}
	if len(d.MissingEnvironments) > 0 {
		return problemf(CodeEnvironmentMissing, "%s", missingEnvironments(d))
	}
	if d.EnvError != "" {
		return problemf(CodeEnvironmentMissing, "the agent's env cannot be resolved on %s: %s", d.Host.Hostname, d.EnvError)
	}
	return nil
}

// missingEnvironments words the refusal for d's missing environments, with
// the reason for each one a followed lead defines but this host isolated.
func missingEnvironments(d DestFacts) string {
	var undefined, invalid []string
	for _, n := range d.MissingEnvironments {
		if reason, ok := d.InvalidEnvironments[n]; ok {
			invalid = append(invalid, fmt.Sprintf("environment %q is invalid on %s: %s", n, d.Host.Hostname, reason))
		} else {
			undefined = append(undefined, n)
		}
	}
	if len(undefined) > 0 {
		invalid = append(invalid, fmt.Sprintf("environment(s) %s are not defined on %s", strings.Join(undefined, ", "), d.Host.Hostname))
	}
	return strings.Join(invalid, "; ")
}

func checkHarness(m Manifest, d DestFacts) *Problem {
	have, want := orClaude(m.Agent.Harness), orClaude(d.TemplateHarness)
	switch {
	case d.TemplateFound && have != want:
		return problemf(CodeHarnessMismatch, "the agent runs on %s but template %q uses %s on %s", have, m.Agent.Template, want, d.Host.Hostname)
	case d.HarnessBinary == "":
		return problemf(CodeHarnessBinaryMissing, "%s is not installed on %s", have, d.Host.Hostname)
	case d.SessionRoot != "" && !d.SessionRootPresent:
		return problemf(CodeSessionRootMissing, "the session store %s does not exist on %s; log %s in there first", d.SessionRoot, d.Host.Hostname, have)
	}
	return nil
}

func orClaude(h string) string {
	if h == "" {
		return "claude"
	}
	return h
}

// checkIdentity covers the target name and workspace.
func checkIdentity(m Manifest, d DestFacts) *Problem {
	name, ws := m.TargetName(), m.TargetWorkspace()
	if err := ValidateRef(name); err != nil {
		return asProblem(err)
	}
	switch {
	case d.Existing.Present && (m.IsCopy() || d.Existing.MovedTo == ""):
		return problemf(CodeNameTaken, "an agent named %s already exists on %s", name, d.Host.Hostname)
	case len(d.WorkspaceUsers) > 0:
		return problemf(CodeWorkspaceInUse, "%s is the workspace of %s on %s", ws, strings.Join(d.WorkspaceUsers, ", "), d.Host.Hostname)
	case d.WSState == WSForeign:
		return problemf(CodeWorkspaceExists, "%s already exists on %s and is not this agent's; move it aside first", ws, d.Host.Hostname)
	}
	return nil
}

func checkDestGit(m Manifest, d DestFacts) []Problem {
	g, dg := m.Git, d.Git
	if dg == nil || dg.GitPath == "" {
		return []Problem{*problemf(CodeGitMissing, "git is not installed on %s", d.Host.Hostname)}
	}
	var ps []Problem
	add := func(p *Problem) {
		if p != nil {
			ps = append(ps, *p)
		}
	}
	add(asProblem(ValidatePath(g.Canonical)))
	if b := m.TargetBranch(); b != "" {
		add(asProblem(ValidateRef(b)))
	}
	switch {
	case !dg.CanonicalPresent && !dg.Cloneable:
		add(problemf(CodeCanonicalMissing, "the repository %s is missing on %s; clone it there first (%s)", g.Canonical, d.Host.Hostname, cloneHint(g)))
	case dg.CanonicalPresent && !dg.HasRoot:
		add(problemf(CodeCanonicalMismatch, "%s on %s is a different repository (no shared root commit)", g.Canonical, d.Host.Hostname))
	}
	if dg.OpInProgress != "" {
		add(problemf(CodeGitOpInProgress, "a %s is in progress in %s on %s; finish or abort it first", dg.OpInProgress, g.Canonical, d.Host.Hostname))
	}
	add(checkDestBranch(m, d))
	return ps
}

func cloneHint(g *GitManifest) string {
	if g.Repo != "" {
		return fmt.Sprintf("gh repo clone %s %s", g.Repo, g.Canonical)
	}
	return fmt.Sprintf("git clone <url> %s", g.Canonical)
}

// checkDestBranch refuses a target branch another worktree holds, and a
// copy branch that already exists (unless it is this copy's own partial).
func checkDestBranch(m Manifest, d DestFacts) *Problem {
	b := m.TargetBranch()
	if b == "" {
		return nil
	}
	ws := m.TargetWorkspace()
	at, held := d.Git.CheckedOut[b]
	if m.IsCopy() {
		partial := ws + PartialSuffix
		if held && git.SamePath(at, partial) {
			return nil
		}
		if held || d.Git.BranchSHA != "" {
			return problemf(CodeBranchExists, "branch %s already exists on %s; pick another with --branch", b, d.Host.Hostname)
		}
		return nil
	}
	if held && !git.SamePath(at, ws) {
		return problemf(CodeBranchCheckedOut, "branch %s is checked out at %s on %s", b, at, d.Host.Hostname)
	}
	return nil
}
