package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git"
)

// baseAgentName is the prefix used for both shared-workspace and worktree agents
// so tmux session enumeration can filter by prefix.
const baseAgentName = "leo"

// maxBranchSlugInName caps how much of the branch slug is embedded in an agent
// name before it is hashed. tmux allows long session names but operators still
// need to type them, so we keep the whole name comfortable in a terminal.
const maxBranchSlugInName = 40

// BaseWorkspace resolves the workspace root for a template, falling back to
// ~/.leo/agents when the template does not pin a location.
func BaseWorkspace(tmpl config.TemplateConfig) string {
	if tmpl.Workspace != "" {
		return tmpl.Workspace
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".leo", "agents")
}

// WorktreeRoot returns the shared root under baseWorkspace where per-agent
// worktrees live: <base>/.worktrees/<repo-short>/<branch-slug>.
func WorktreeRoot(baseWorkspace string) string {
	return filepath.Join(baseWorkspace, ".worktrees")
}

// WorktreeLayout describes all paths and the derived agent name for a
// worktree-mode spawn. Consumers that only need a subset (e.g. the agent name
// for collision checks) still get the whole struct so the logic lives in one
// place.
type WorktreeLayout struct {
	// CanonicalPath is the clone used as the git origin for every worktree on
	// this repo. Shared across worktree agents; never deleted by Prune.
	CanonicalPath string
	// WorktreePath is the per-agent worktree directory. This is what claude
	// will cd into.
	WorktreePath string
	// Branch is the real git branch name (preserved verbatim from user input).
	Branch string
	// BranchSlug is the filesystem-safe form of Branch.
	BranchSlug string
	// AgentName is the fully qualified agent/tmux session name.
	AgentName string
}

// ResolveWorkspace determines the workspace path and agent name for a shared
// (non-worktree) spawn. Preserves historical behavior: owner/repo is cloned
// into <base>/<repo-short> with `gh`; a bare name uses the base workspace
// directly.
func ResolveWorkspace(tmpl config.TemplateConfig, templateName, repo, nameOverride string) (workspace, agentName string, err error) {
	base := BaseWorkspace(tmpl)
	if strings.Contains(repo, "/") {
		canonical, err := EnsureCanonical(base, repo)
		if err != nil {
			return "", "", err
		}
		owner, repoShort := splitRepo(repo)
		name := fmt.Sprintf("%s-%s-%s-%s", baseAgentName, templateName, owner, repoShort)
		if nameOverride != "" {
			name = nameOverride
		}
		return canonical, name, nil
	}

	if err := os.MkdirAll(base, 0750); err != nil {
		return "", "", fmt.Errorf("creating workspace dir: %w", err)
	}
	name := fmt.Sprintf("%s-%s-%s", baseAgentName, templateName, repo)
	if nameOverride != "" {
		name = nameOverride
	}
	return base, name, nil
}

// EnsureCanonical ensures a canonical clone of owner/repo exists under
// baseWorkspace. Returns the canonical path. A pre-existing .git directory is
// taken as proof the clone is ready; this function never re-clones.
func EnsureCanonical(baseWorkspace, repo string) (string, error) {
	owner, repoShort := splitRepo(repo)
	if owner == "" || repoShort == "" {
		return "", fmt.Errorf("repo %q is not owner/repo", repo)
	}
	canonical := filepath.Join(baseWorkspace, repoShort)
	if _, err := os.Stat(filepath.Join(canonical, ".git")); err == nil {
		return canonical, nil
	}
	if err := os.MkdirAll(baseWorkspace, 0750); err != nil {
		return "", fmt.Errorf("creating workspace dir: %w", err)
	}
	ghPath, lookErr := exec.LookPath("gh")
	if lookErr != nil {
		return "", fmt.Errorf("gh CLI not found — install with: brew install gh")
	}
	cmd := exec.Command(ghPath, "repo", "clone", repo, canonical)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		return "", fmt.Errorf("cloning %s: %s", repo, strings.TrimSpace(string(output)))
	}
	return canonical, nil
}

// ResolveWorktreeLayout computes every path and the derived agent name for a
// worktree spawn. It does not touch the filesystem or shell out — callers are
// expected to pass the canonical path from EnsureCanonical. The returned
// AgentName honors nameOverride.
func ResolveWorktreeLayout(baseWorkspace, canonicalPath, templateName, repo, branch, nameOverride string) (WorktreeLayout, error) {
	if !strings.Contains(repo, "/") {
		return WorktreeLayout{}, ErrWorktreeRequiresSlash
	}
	if branch == "" {
		return WorktreeLayout{}, fmt.Errorf("worktree spawn requires a branch name")
	}
	slug, err := git.SlugifyBranch(branch)
	if err != nil {
		return WorktreeLayout{}, fmt.Errorf("computing branch slug: %w", err)
	}

	owner, repoShort := splitRepo(repo)
	worktreePath := filepath.Join(WorktreeRoot(baseWorkspace), repoShort, slug)

	name := nameOverride
	if name == "" {
		boundedSlug := git.BoundedSlug(slug, maxBranchSlugInName)
		name = fmt.Sprintf("%s-%s-%s-%s-%s", baseAgentName, templateName, owner, repoShort, boundedSlug)
	}

	return WorktreeLayout{
		CanonicalPath: canonicalPath,
		WorktreePath:  worktreePath,
		Branch:        branch,
		BranchSlug:    slug,
		AgentName:     name,
	}, nil
}

// AddWorktreeForBranch runs `git worktree add` against canonical in a way that
// matches the current state of the branch:
//   - exists locally → attach to existing branch
//   - exists only on origin → create local tracking branch
//   - does not exist anywhere → create new branch off baseRef
//
// Fetch is the caller's responsibility so the caller can choose to skip it in
// offline modes (not yet implemented; today the CLI always fetches).
func AddWorktreeForBranch(ctx context.Context, canonical, worktreePath, branch, baseRef string) error {
	status, err := git.GetBranchStatus(ctx, canonical, branch)
	if err != nil {
		return fmt.Errorf("checking branch status: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0750); err != nil {
		return fmt.Errorf("creating worktree parent dir: %w", err)
	}
	switch {
	case status.Local:
		return git.AddWorktreeExisting(ctx, canonical, worktreePath, branch)
	case status.Remote:
		return git.AddWorktreeTracking(ctx, canonical, worktreePath, branch)
	default:
		if baseRef == "" {
			def, err := git.DefaultBranch(ctx, canonical)
			if err != nil {
				return fmt.Errorf("resolving base ref: %w", err)
			}
			baseRef = def
		}
		return git.AddWorktreeNew(ctx, canonical, worktreePath, branch, baseRef)
	}
}

// splitRepo returns (owner, repoShort) for a "owner/repo" input. Returns
// ("", value) for a slashless value so callers can detect shared-workspace mode.
func splitRepo(repo string) (string, string) {
	idx := strings.Index(repo, "/")
	if idx < 0 {
		return "", repo
	}
	return repo[:idx], repo[idx+1:]
}
