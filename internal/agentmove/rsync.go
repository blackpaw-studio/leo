package agentmove

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/git"
)

// safePath is the only shape a path on rsync's or git's command line may
// take: openrsync has no --protect-args, so the remote shell sees it.
var safePath = regexp.MustCompile(`^/[A-Za-z0-9._@%+=,/-]+$`)

// ValidatePath refuses a path that is not absolute, has a .. component, or
// holds anything outside the safe charset. A failure is unsafe_path.
func ValidatePath(p string) error {
	if !safePath.MatchString(p) {
		return problemf(CodeUnsafePath, "unsafe path %q: moves need absolute paths of [A-Za-z0-9._@%%+=,/-] only", p)
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return problemf(CodeUnsafePath, "unsafe path %q: .. component", p)
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

// SessionRsyncCmd sends one session path (src, symlinks already resolved;
// a trailing "/" sends a directory's contents) into dst, the real directory
// the destination resolved for it. There is no -R: rsync never creates, and
// so never replaces, an implied parent such as a symlinked projects/. (-K
// would keep it, but openrsync on macOS fails with it.) --update never
// overwrites a newer destination file, and nothing is deleted.
func SessionRsyncCmd(shell, src, dst string) Cmd {
	args := withShell([]string{"-a", "-u", "--itemize-changes"}, shell)
	return Cmd{Name: "rsync", Args: append(args, src, strings.TrimSuffix(dst, "/")+"/")}
}

// StagingRsyncArgv mirrors the staging dir (hook output, harness exports).
func StagingRsyncArgv(shell, stage, target string) []string {
	return append(withShell([]string{"-a", "--delete", "--itemize-changes"}, shell), stage+"/", target+"/")
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
	SubmoduleAbsorbed = git.SubmoduleAbsorbed
	SubmoduleInline   = git.SubmoduleInline
	SubmoduleUninit   = git.SubmoduleUninit
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
	// DestNested are the destination's own paths inside the workspace,
	// relative to it, kept from --delete.
	DestNested []string
}

// Excludes returns the workspace rsync's exclude rules, in order: the user's,
// /.worktrees/, nested agent workspaces, the destination's nested paths,
// then the git-mode rules.
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
	for _, p := range w.DestNested {
		out = append(out, "/"+p+"/")
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

// MaxItemizedLines caps the itemized changes a stage keeps in the result.
const MaxItemizedLines = 200

// itemized matches one --itemize-changes line: a deletion, or an update
// flag block before the path: update type, file type, then nine attribute
// flags (GNU rsync) or seven (openrsync, macOS's /usr/bin/rsync).
var itemized = regexp.MustCompile(`^(\*deleting\s|[<>ch.][fdLDS][.+?a-zA-Z]{7,9}\s)`)

// ItemizedLines picks the itemized change lines out of rsync's stdout,
// dropping the file-list banner and --stats summary.
func ItemizedLines(out []byte) []string {
	lines := []string{}
	for _, l := range bytes.Split(out, []byte("\n")) {
		if itemized.Match(l) {
			lines = append(lines, string(l))
		}
	}
	return lines
}

// RsyncStage is one rsync stage's itemized changes: the first
// MaxItemizedLines of Total, Truncated being the rest.
type RsyncStage struct {
	Stage     string   `json:"stage"`
	Total     int      `json:"total"`
	Lines     []string `json:"lines"`
	Truncated int      `json:"truncated,omitempty"`
}

func newRsyncStage(stage string, lines []string) RsyncStage {
	st := RsyncStage{Stage: stage, Lines: []string{}}
	st.add(lines)
	return st
}

// add records more itemized lines, keeping only up to the cap.
func (s *RsyncStage) add(lines []string) {
	s.Total += len(lines)
	room := MaxItemizedLines - len(s.Lines)
	if room > len(lines) {
		room = len(lines)
	}
	s.Lines = append(s.Lines, lines[:room]...)
	s.Truncated = s.Total - len(s.Lines)
}
