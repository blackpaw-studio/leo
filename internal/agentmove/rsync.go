package agentmove

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// safePath is the only shape a path on rsync's or git's command line may
// take: openrsync has no --protect-args, so the remote shell sees it.
var safePath = regexp.MustCompile(`^/[A-Za-z0-9._@%+=,/-]+$`)

// ValidatePath refuses a path that is not absolute, has a .. component, or
// holds anything outside the safe charset. A failure is unsafe_path.
func ValidatePath(p string) error {
	if !safePath.MatchString(p) {
		return fmt.Errorf("unsafe path %q: moves need absolute paths of [A-Za-z0-9._@%%+=,/-] only", p)
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return fmt.Errorf("unsafe path %q: .. component", p)
		}
	}
	return nil
}

// withShell inserts -e shell when the endpoint is remote.
func withShell(args []string, shell string) []string {
	if shell == "" {
		return args
	}
	return append(args, "-e", shell)
}

// WorkspaceRsyncArgv sends the workspace: the source is authoritative, so
// --delete removes what the source no longer has, and only changed files
// cross. Excluded paths are neither sent nor deleted.
func WorkspaceRsyncArgv(shell, ws, target string, excludes []string) []string {
	args := withShell([]string{"-a", "--delete", "--itemize-changes", "--stats"}, shell)
	for _, x := range excludes {
		args = append(args, "--exclude="+x)
	}
	return append(args, ws+"/", target+"/")
}

// SessionRsyncArgv sends session files by path relative to root (-R with a
// /./ anchor maps between differing roots), --update so a newer file on the
// destination is never overwritten, and never deletes.
func SessionRsyncArgv(shell, root string, rels []string, target string) []string {
	args := withShell([]string{"-a", "-R", "-u", "--itemize-changes"}, shell)
	for _, rel := range rels {
		args = append(args, root+"/./"+rel)
	}
	return append(args, target+"/")
}

// StagingRsyncArgv mirrors the staging dir (hook output, harness exports).
func StagingRsyncArgv(shell, stage, target string) []string {
	return append(withShell([]string{"-a", "--delete"}, shell), stage+"/", target+"/")
}

// ModulesRsyncArgv mirrors the agent's private submodule git dirs (S5m).
func ModulesRsyncArgv(shell, src, target string) []string {
	return append(withShell([]string{"-a", "--delete", "--itemize-changes"}, shell), src+"/", target+"/")
}

// ModulesPartial is where a copy's submodule git dirs land until G4 renames
// them into place (git refuses to move a worktree once modules/ exists).
const ModulesPartial = "leo-modules.partial"

// ModulesTarget is S5m's destination: the destination modules dir for a
// move, or <gitdir>/leo-modules.partial for a copy.
func ModulesTarget(isCopy bool, destModulesDir, destGitDir string) string {
	if isCopy {
		return filepath.Join(destGitDir, ModulesPartial)
	}
	return destModulesDir
}

// Submodule states (see git.Submodules).
const (
	SubmoduleAbsorbed = "absorbed"
	SubmoduleInline   = "inline"
	SubmoduleUninit   = "uninit"
)

// Submodule is one submodule as the workspace transfer sees it.
type Submodule struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

// WorkspaceSpec is what decides the workspace rsync's excludes.
type WorkspaceSpec struct {
	Workspace string
	// UserExcludes are move.exclude, global then template.
	UserExcludes []string
	// Nested are other local agents' workspaces; those under Workspace are
	// excluded.
	Nested []string
	// GitMode keeps the destination's own .git (a worktree pointer or a
	// shared canonical) out of the transfer.
	GitMode bool
	// Submodules' absorbed .git files are excluded: G4 rewrites them.
	Submodules []Submodule
	// NestedWorktrees are foreign linked worktrees inside the workspace,
	// relative to it, skipped with a note.
	NestedWorktrees []string
}

// Excludes returns the workspace rsync's exclude rules, in order: the user's,
// /.worktrees/, nested agent workspaces, then the git-mode rules.
func (w WorkspaceSpec) Excludes() []string {
	out := append([]string{}, w.UserExcludes...)
	out = append(out, "/.worktrees/")
	for _, n := range w.Nested {
		rel, err := filepath.Rel(w.Workspace, n)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		out = append(out, "/"+rel+"/")
	}
	if !w.GitMode {
		return out
	}
	out = append(out, "/.git")
	for _, s := range w.Submodules {
		if s.State == SubmoduleAbsorbed {
			out = append(out, "/"+s.Path+"/.git")
		}
	}
	for _, p := range w.NestedWorktrees {
		out = append(out, "/"+strings.Trim(p, "/")+"/")
	}
	return out
}
