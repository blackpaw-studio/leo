package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// ResolveWorkspace determines the workspace path and agent name for a given template + repo.
// If repo contains "/", it is treated as owner/repo and cloned with `gh` if not already present.
// Otherwise the template workspace is used directly and repo is treated as an agent-name suffix.
// nameOverride, if non-empty, replaces the derived agent name.
func ResolveWorkspace(tmpl config.TemplateConfig, templateName, repo, nameOverride string) (workspace, agentName string, err error) {
	baseWorkspace := tmpl.Workspace
	if baseWorkspace == "" {
		home, _ := os.UserHomeDir()
		baseWorkspace = filepath.Join(home, ".leo", "agents")
	}

	if strings.Contains(repo, "/") {
		parts := strings.SplitN(repo, "/", 2)
		owner := parts[0]
		repoShort := parts[1]

		workspace = filepath.Join(baseWorkspace, repoShort)
		agentName = fmt.Sprintf("leo-%s-%s-%s", templateName, owner, repoShort)
		if nameOverride != "" {
			agentName = nameOverride
		}

		if _, statErr := os.Stat(filepath.Join(workspace, ".git")); statErr != nil {
			if err := os.MkdirAll(baseWorkspace, 0750); err != nil {
				return "", "", fmt.Errorf("creating workspace dir: %w", err)
			}
			ghPath, lookErr := exec.LookPath("gh")
			if lookErr != nil {
				return "", "", fmt.Errorf("gh CLI not found — install with: brew install gh")
			}
			cmd := exec.Command(ghPath, "repo", "clone", repo, workspace)
			if output, runErr := cmd.CombinedOutput(); runErr != nil {
				return "", "", fmt.Errorf("cloning %s: %s", repo, strings.TrimSpace(string(output)))
			}
		}
		return workspace, agentName, nil
	}

	workspace = baseWorkspace
	agentName = fmt.Sprintf("leo-%s-%s", templateName, repo)
	if nameOverride != "" {
		agentName = nameOverride
	}
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return "", "", fmt.Errorf("creating workspace dir: %w", err)
	}
	return workspace, agentName, nil
}
