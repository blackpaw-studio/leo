package agent

import (
	"strings"
	"testing"
)

func TestResolveAgentWorktreeLayout(t *testing.T) {
	layout, err := ResolveAgentWorktreeLayout("/base", "/base/chronicle", "chronicle", "a11y", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if layout.AgentName != "chronicle-a11y" {
		t.Errorf("AgentName = %q, want chronicle-a11y", layout.AgentName)
	}
	if layout.WorktreePath != "/base/.worktrees/chronicle/a11y" {
		t.Errorf("WorktreePath = %q", layout.WorktreePath)
	}
	if layout.CanonicalPath != "/base/chronicle" {
		t.Errorf("CanonicalPath = %q", layout.CanonicalPath)
	}
	if layout.Branch != "a11y" || layout.BranchSlug != "a11y" {
		t.Errorf("Branch/BranchSlug = %q/%q", layout.Branch, layout.BranchSlug)
	}
}

func TestResolveAgentWorktreeLayout_SlugsBranch(t *testing.T) {
	layout, err := ResolveAgentWorktreeLayout("/base", "/c", "leo", "feat/new-endpoint", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Same slug scheme as owner/repo worktrees (git.SlugifyBranch).
	if strings.Contains(layout.WorktreePath, "/feat/new-endpoint") {
		t.Errorf("branch was not slugged in path: %q", layout.WorktreePath)
	}
	if layout.AgentName == "" || strings.Contains(layout.AgentName, "/") {
		t.Errorf("bad AgentName %q", layout.AgentName)
	}
}

func TestResolveAgentWorktreeLayout_NameOverride(t *testing.T) {
	layout, err := ResolveAgentWorktreeLayout("/base", "/c", "leo", "x", "custom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if layout.AgentName != "custom" {
		t.Errorf("AgentName = %q, want custom", layout.AgentName)
	}
}

func TestResolveAgentWorktreeLayout_Errors(t *testing.T) {
	if _, err := ResolveAgentWorktreeLayout("/base", "/c", "", "x", ""); err == nil {
		t.Error("expected error for empty source agent")
	}
	if _, err := ResolveAgentWorktreeLayout("/base", "/c", "leo", "", ""); err == nil {
		t.Error("expected error for empty branch")
	}
}
