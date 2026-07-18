// Package-file: codex's Seatbelt workspace-write sandbox marks `.git`,
// `.agents`, and `.codex` under each writable root read-only via per-root
// require-not clauses (verified empirically with `codex sandbox`,
// codex-cli 0.144.5). That breaks `git add` (cannot create
// .git/index.lock) in an ordinary checkout, breaks it again in a linked
// git worktree (the real git dir lives outside the workspace entirely, at
// <canonical>/.git/worktrees/<name>), and breaks `mkdir .agents` for
// project-skill creation even when the directory doesn't exist yet.
// Seatbelt ORs allow rules, so passing the git metadata dirs and the
// `.agents` dir as extra writable_roots restores access; these config
// overrides are inert when the sandbox mode is read-only or
// danger-full-access, so they're safe to emit unconditionally.
package codex

import (
	"os"
	"path/filepath"
	"strings"
)

// sandboxWritableRootsArgs renders the `-c
// sandbox_workspace_write.writable_roots=[...]` override that restores
// write access to the workspace's git metadata (checkout .git dir, or the
// resolved gitdir + commondir for a linked worktree) and its .agents
// directory (added unconditionally so first-time project-skill creation
// isn't blocked). Empty workspace returns nil defensively.
func sandboxWritableRootsArgs(workspace string) []string {
	if workspace == "" {
		return nil
	}
	roots := append([]string{resolvePath(filepath.Join(workspace, ".agents"))}, gitMetaDirs(workspace)...)
	return []string{"-c", "sandbox_workspace_write.writable_roots=" + tomlStringArray(dedupe(roots))}
}

// gitMetaDirs resolves the git metadata dir(s) for workspace by pure file
// parsing (no git subprocess). Returns nil for any non-repo or
// unparseable workspace — that's the normal case for leo workspaces, most
// of which aren't git repos at all.
func gitMetaDirs(workspace string) []string {
	gitPath := filepath.Join(workspace, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		return []string{resolvePath(gitPath)}
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	return parseGitLinkFile(workspace, gitPath)
}

// parseGitLinkFile parses a worktree/submodule `.git` file (first line
// `gitdir: <path>`), then follows its optional `commondir` file to the
// canonical shared git dir.
func parseGitLinkFile(workspace, gitPath string) []string {
	data, err := os.ReadFile(gitPath) // #nosec G304 -- path derived from workspace, not user input
	if err != nil {
		return nil
	}
	firstLine := strings.TrimSpace(firstLineOf(string(data)))
	const prefix = "gitdir: "
	if !strings.HasPrefix(firstLine, prefix) {
		return nil
	}
	gitDirRaw := strings.TrimSpace(strings.TrimPrefix(firstLine, prefix))
	if gitDirRaw == "" {
		return nil
	}
	gitDir := gitDirRaw
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workspace, gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	dirs := []string{resolvePath(gitDir)}
	if commonDir, ok := readCommonDir(gitDir); ok {
		dirs = append(dirs, resolvePath(commonDir))
	}
	return dedupe(dirs)
}

// readCommonDir reads gitDir's optional commondir pointer file (present
// for linked worktrees), resolving a relative value against gitDir.
func readCommonDir(gitDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir")) // #nosec G304 -- path derived from workspace, not user input
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(firstLineOf(string(data)))
	if line == "" {
		return "", false
	}
	commonDir := line
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	return filepath.Clean(commonDir), true
}

// firstLineOf returns s up to (not including) the first newline.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// resolvePath canonicalizes p via filepath.EvalSymlinks, matching codex's
// own path canonicalization (e.g. macOS /tmp -> /private/tmp; see
// ensureWorkspaceTrusted in driver.go). p itself may not exist yet (e.g. an
// as-yet-uncreated .agents dir), so symlink resolution walks up to the
// nearest existing ancestor and rejoins the remainder unresolved.
func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolvePath(parent), filepath.Base(p))
}

// dedupe removes repeated entries while preserving first-seen order.
func dedupe(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		if seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	return out
}
