package agentmove

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

func studio() Host { return Host{Hostname: "studio", Home: "/Users/me", User: "me"} }

func moveManifest() Manifest {
	return Manifest{
		Protocol: Protocol, Mode: ModeMove, From: studio(),
		Agent: agentstore.Record{Name: "web", Template: "coder", Harness: "claude", Workspace: "/Users/me/.leo/agents/web", Environments: []string{"work"}},
	}
}

func okFacts() DestFacts {
	return DestFacts{
		Host: Host{Hostname: "laptop", Home: "/Users/me", User: "me"}, TemplateFound: true, TemplateHarness: "claude",
		HarnessBinary: "/usr/local/bin/claude", SessionRoot: "/Users/me/.claude-work", SessionRootPresent: true,
		WSState: WSAbsent, StagingDir: "/Users/me/.leo/state/moves/web/staging",
	}
}

func codes(ps []Problem) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Code
	}
	return strings.Join(out, ",")
}

func TestCheckDestination(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest, *DestFacts)
		want   string // comma-joined codes, "" for none
	}{
		{"ok", func(*Manifest, *DestFacts) {}, ""},
		{"protocol", func(m *Manifest, _ *DestFacts) { m.Protocol = Protocol + 1 }, CodeProtocolMismatch},
		{"home", func(_ *Manifest, d *DestFacts) { d.Host.Home = "/home/me" }, CodeHomeMismatch},
		{"user", func(_ *Manifest, d *DestFacts) { d.Host.User = "other" }, CodeUserMismatch},
		{"template", func(_ *Manifest, d *DestFacts) { d.TemplateFound = false }, CodeTemplateMissing},
		{"environment", func(_ *Manifest, d *DestFacts) { d.MissingEnvironments = []string{"work"} }, CodeEnvironmentMissing},
		{"harness", func(_ *Manifest, d *DestFacts) { d.TemplateHarness = "codex" }, CodeHarnessMismatch},
		{"harness default is claude", func(m *Manifest, d *DestFacts) { m.Agent.Harness = ""; d.TemplateHarness = "" }, ""},
		{"binary", func(_ *Manifest, d *DestFacts) { d.HarnessBinary = "" }, CodeHarnessBinaryMissing},
		{"session root", func(_ *Manifest, d *DestFacts) { d.SessionRootPresent = false }, CodeSessionRootMissing},
		{"no session root needed", func(_ *Manifest, d *DestFacts) { d.SessionRoot = ""; d.SessionRootPresent = false }, ""},
		{"name taken", func(_ *Manifest, d *DestFacts) { d.Existing = Existing{Present: true} }, CodeNameTaken},
		{"moved shadow accepted", func(_ *Manifest, d *DestFacts) { d.Existing = Existing{Present: true, MovedTo: "studio"} }, ""},
		{"copy name taken even by a shadow", func(m *Manifest, d *DestFacts) {
			m.Mode = ModeCopy
			m.Copy = &CopySpec{Name: "web-copy", Workspace: "/Users/me/.leo/agents/web-copy"}
			d.Existing = Existing{Present: true, MovedTo: "studio"}
		}, CodeNameTaken},
		{"workspace in use", func(_ *Manifest, d *DestFacts) { d.WorkspaceUsers = []string{"api"} }, CodeWorkspaceInUse},
		{"workspace foreign", func(_ *Manifest, d *DestFacts) { d.WSState = WSForeign }, CodeWorkspaceExists},
		{"workspace ours", func(_ *Manifest, d *DestFacts) { d.WSState = WSOurs }, ""},
		{"unsafe workspace", func(m *Manifest, _ *DestFacts) { m.Agent.Workspace = "/Users/me/my agent" }, CodeUnsafePath},
		{"unsafe session root", func(_ *Manifest, d *DestFacts) { d.SessionRoot = "/Users/me/.claude $(x)" }, CodeUnsafePath},
		{"unsafe name", func(m *Manifest, _ *DestFacts) { m.Agent.Name = "-rf" }, CodeUnsafeRef},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, d := moveManifest(), okFacts()
			tt.mutate(&m, &d)
			got := CheckDestination(m, d)
			if codes(got) != tt.want {
				t.Fatalf("problems = %+v, want codes %q", got, tt.want)
			}
			for _, p := range got {
				if p.Message == "" {
					t.Errorf("%s has no message", p.Code)
				}
			}
		})
	}
}

func gitManifest(mode string) Manifest {
	m := moveManifest()
	m.Agent.Branch, m.Agent.CanonicalPath = "feat", "/Users/me/src/app"
	m.Agent.Workspace = "/Users/me/.leo/agents/.worktrees/app/feat"
	m.Git = &GitManifest{Mode: mode, Canonical: "/Users/me/src/app", HeadRef: "refs/heads/feat", HeadSHA: "aaa", RootSHAs: []string{"r1"}, Repo: "me/app"}
	return m
}

func gitFacts() DestFacts {
	d := okFacts()
	d.Git = &GitPreflight{GitPath: "/usr/bin/git", CanonicalPresent: true, HasRoot: true, CheckedOut: map[string]string{"main": "/Users/me/src/app"}}
	return d
}

func TestCheckDestinationGitCodes(t *testing.T) {
	ws := "/Users/me/.leo/agents/.worktrees/app/feat"
	tests := []struct {
		name   string
		mutate func(*Manifest, *DestFacts)
		want   string
	}{
		{"ok", func(*Manifest, *DestFacts) {}, ""},
		{"git missing", func(_ *Manifest, d *DestFacts) { d.Git.GitPath = "" }, CodeGitMissing},
		{"canonical seeded", func(_ *Manifest, d *DestFacts) {
			d.Git.CanonicalPresent, d.Git.HasRoot, d.Git.Cloneable = false, false, true
		}, ""},
		{"canonical missing", func(_ *Manifest, d *DestFacts) { d.Git.CanonicalPresent, d.Git.HasRoot = false, false }, CodeCanonicalMissing},
		{"canonical mismatch", func(_ *Manifest, d *DestFacts) { d.Git.HasRoot = false }, CodeCanonicalMismatch},
		{"op in progress", func(_ *Manifest, d *DestFacts) { d.Git.OpInProgress = "rebase" }, CodeGitOpInProgress},
		{"branch held elsewhere", func(_ *Manifest, d *DestFacts) { d.Git.CheckedOut["feat"] = "/Users/me/other" }, CodeBranchCheckedOut},
		{"branch at our ws", func(_ *Manifest, d *DestFacts) { d.Git.CheckedOut["feat"] = ws; d.WSState = WSOurs }, ""},
		{"ws foreign", func(_ *Manifest, d *DestFacts) { d.WSState = WSForeign }, CodeWorkspaceExists},
		{"unsafe branch", func(m *Manifest, _ *DestFacts) { m.Git.HeadRef = "refs/heads/a..b" }, CodeUnsafeRef},
		{"unsafe canonical", func(m *Manifest, _ *DestFacts) { m.Git.Canonical = "/Users/me/src/my app" }, CodeUnsafePath},
		{"detached head", func(m *Manifest, _ *DestFacts) { m.Git.HeadRef = "" }, ""},
		{"plain mode skips git codes", func(m *Manifest, d *DestFacts) { m.Git.Mode = GitModePlain; d.Git.GitPath = "" }, ""},
		{"copy branch exists", func(m *Manifest, d *DestFacts) {
			m.Mode, m.Copy = ModeCopy, &CopySpec{Name: "feat-x", Workspace: ws + "-feat-x", Branch: "feat-feat-x"}
			d.Git.BranchSHA = "bbb"
		}, CodeBranchExists},
		{"copy reuses its partial", func(m *Manifest, d *DestFacts) {
			m.Mode, m.Copy = ModeCopy, &CopySpec{Name: "feat-x", Workspace: ws + "-feat-x", Branch: "feat-feat-x"}
			d.Git.BranchSHA = "bbb"
			d.Git.CheckedOut["feat-feat-x"] = ws + "-feat-x" + PartialSuffix
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, d := gitManifest(GitModeLinked), gitFacts()
			tt.mutate(&m, &d)
			if got := CheckDestination(m, d); codes(got) != tt.want {
				t.Fatalf("problems = %+v, want codes %q", got, tt.want)
			}
		})
	}
	m, d := gitManifest(GitModeLinked), gitFacts()
	d.Git.CanonicalPresent, d.Git.HasRoot = false, false
	if got := CheckDestination(m, d); len(got) != 1 || !strings.Contains(got[0].Message, "gh repo clone me/app /Users/me/src/app") {
		t.Fatalf("canonical_missing message = %+v, want the clone command", got)
	}
}

func TestValidatorsReturnCodes(t *testing.T) {
	if err := ValidatePath("/a b"); !HasCode(err, CodeUnsafePath) {
		t.Errorf("ValidatePath = %v, want unsafe_path", err)
	}
	if err := ValidateRef("a..b"); !HasCode(err, CodeUnsafeRef) {
		t.Errorf("ValidateRef = %v, want unsafe_ref", err)
	}
}
