package agentmove

import (
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/git"
)

// Protocol is the move wire protocol version; both ends must speak it.
const Protocol = 1

// Transfer modes.
const (
	ModeMove = "move"
	ModeCopy = "copy"
)

// PartialSuffix marks a copy's workspace while it is being filled; commit
// renames it into place, and a failed run's partial is reused.
const PartialSuffix = ".leo-partial"

// Destination workspace states.
const (
	// WSAbsent: nothing at the workspace path (an empty dir counts).
	WSAbsent = "absent"
	// WSOurs: this agent's own leftover (a moved shadow's workspace, or a
	// linked worktree of the canonical with no live record): a move back or
	// a re-run.
	WSOurs = "ours"
	// WSForeign: something else lives there.
	WSForeign = "foreign"
)

// Host identifies one end of a transfer. Paths are identical on both hosts,
// so Home must match.
type Host struct {
	Hostname string `json:"hostname"`
	Home     string `json:"home"`
	User     string `json:"user"`
}

// CopySpec is a copy's new identity on the destination.
type CopySpec struct {
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	// Branch is the copy's superproject branch, "" outside git.
	Branch string `json:"branch,omitempty"`
	// SessionID is the fork's pre-issued id (claude), "" when the harness
	// discovers it after the first turn.
	SessionID string `json:"session_id,omitempty"`
}

// Upstream is a branch's tracking config.
type Upstream struct {
	Remote string `json:"remote"`
	Merge  string `json:"merge"`
}

// GitManifest is the source's git state. Mode plain still travels so the
// destination can report linked worktrees at the workspace (which turns the
// move into main mode).
type GitManifest struct {
	Mode      string `json:"mode"`
	Canonical string `json:"canonical"`
	// HeadRef is the full branch ref, "" when detached.
	HeadRef     string    `json:"head_ref,omitempty"`
	HeadSHA     string    `json:"head_sha,omitempty"`
	IndexCommit string    `json:"index_commit,omitempty"`
	RootSHAs    []string  `json:"root_shas,omitempty"`
	Upstream    *Upstream `json:"upstream,omitempty"`
	// Repo is owner/repo when the canonical can be cloned with gh.
	Repo string `json:"repo,omitempty"`
	// ModulesDir is the source's absolute private modules dir.
	ModulesDir string `json:"modules_dir,omitempty"`
	// Submodules carry GitDir relative to ModulesDir.
	Submodules []git.Submodule `json:"submodules,omitempty"`
}

// Branch is HeadRef without refs/heads/, "" when detached.
func (g GitManifest) Branch() string { return strings.TrimPrefix(g.HeadRef, "refs/heads/") }

// Manifest is what the source sends with every destination verb.
type Manifest struct {
	Protocol int    `json:"protocol"`
	Mode     string `json:"mode"`
	From     Host   `json:"from"`
	// Agent is the source record, Portable.
	Agent           agentstore.Record `json:"agent"`
	SourceSessionID string            `json:"source_session_id,omitempty"`
	// LastMove is the previous move between this pair of hosts.
	LastMove *time.Time   `json:"last_move,omitempty"`
	Copy     *CopySpec    `json:"copy,omitempty"`
	Git      *GitManifest `json:"git,omitempty"`
}

// IsCopy reports a copy.
func (m Manifest) IsCopy() bool { return m.Mode == ModeCopy && m.Copy != nil }

// TargetName is the agent's name on the destination.
func (m Manifest) TargetName() string {
	if m.IsCopy() {
		return m.Copy.Name
	}
	return m.Agent.Name
}

// TargetWorkspace is the agent's workspace on the destination.
func (m Manifest) TargetWorkspace() string {
	if m.IsCopy() {
		return m.Copy.Workspace
	}
	return m.Agent.Workspace
}

// TargetBranch is the superproject branch the destination checks out, ""
// when detached or outside git.
func (m Manifest) TargetBranch() string {
	if m.IsCopy() {
		return m.Copy.Branch
	}
	if m.Git == nil {
		return ""
	}
	return m.Git.Branch()
}

// gitMode reports whether m moves in main or linked mode.
func (m Manifest) gitMode() bool { return m.Git != nil && m.Git.Mode != GitModePlain }

// Existing is the destination's record under the target name.
type Existing struct {
	Present bool   `json:"present"`
	MovedTo string `json:"moved_to,omitempty"`
	Live    bool   `json:"live,omitempty"`
}

// GitPreflight is the destination's view of the canonical and target branch.
type GitPreflight struct {
	// GitPath is the destination's git binary, "" when none.
	GitPath          string `json:"git_path"`
	CanonicalPresent bool   `json:"canonical_present"`
	// Cloneable: the canonical is missing but gh can clone Repo.
	Cloneable bool `json:"cloneable,omitempty"`
	// HasRoot: the canonical holds one of the source's root commits.
	HasRoot bool `json:"has_root"`
	// BranchSHA is the target branch's commit, "" when it does not exist.
	BranchSHA string `json:"branch_sha,omitempty"`
	// CheckedOut maps each checked-out branch to its worktree path.
	CheckedOut      map[string]string `json:"checked_out,omitempty"`
	LinkedWorktrees int               `json:"linked_worktrees"`
	// OpInProgress names an unfinished git operation in the canonical.
	OpInProgress string `json:"op_in_progress,omitempty"`
}

// DestFacts is what the destination observed about itself for a manifest;
// CheckDestination judges them.
type DestFacts struct {
	Host                Host
	TemplateFound       bool
	TemplateHarness     string
	MissingEnvironments []string
	// HarnessBinary is the resolved harness executable, "" when missing.
	HarnessBinary string
	// SessionRoot is the harness session store for the agent's env here,
	// "" when the harness keeps nothing there.
	SessionRoot        string
	SessionRootPresent bool
	Existing           Existing
	// WorkspaceUsers are other agents whose workspace is the target.
	WorkspaceUsers []string
	WSState        string
	StagingDir     string
	Git            *GitPreflight
}

// PreflightResponse answers a preflight: the facts plus the verdict.
type PreflightResponse struct {
	Protocol    int           `json:"protocol"`
	Host        Host          `json:"host"`
	LeoVersion  string        `json:"leo_version"`
	OK          bool          `json:"ok"`
	Problems    []Problem     `json:"problems,omitempty"`
	SessionRoot string        `json:"session_root,omitempty"`
	StagingDir  string        `json:"staging_dir"`
	Existing    Existing      `json:"existing"`
	WSState     string        `json:"ws_state"`
	Git         *GitPreflight `json:"git,omitempty"`
}
