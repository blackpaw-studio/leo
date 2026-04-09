package onboard

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/setup"
)

var (
	execCommand        = exec.Command
	checkClaudeFn      = prereq.CheckClaude
	setupInteractiveFn = setup.RunInteractive
	newReaderFn        = prompt.NewReader
)

// Run executes the onboarding flow.
func Run() error {
	reader := newReaderFn()

	// 1. Welcome
	fmt.Println()
	prompt.Bold.Println("  Welcome to Leo")
	fmt.Println()
	fmt.Println("  Leo manages Claude Code processes as persistent personal")
	fmt.Println("  assistants with Telegram integration and cron scheduling.")
	fmt.Println()
	fmt.Println("  Let's get you set up.")
	fmt.Println()

	// 2. Prerequisites
	prompt.Bold.Println("Checking prerequisites...")
	fmt.Println()

	claude := checkClaudeFn()
	if !claude.OK {
		prompt.Err.Println("    claude CLI    ✗ not found")
		fmt.Println()
		fmt.Println("  Claude Code CLI is required. Install it:")
		fmt.Println()
		fmt.Println("    brew install claude-code")
		fmt.Println("    — or —")
		fmt.Println("    npm install -g @anthropic-ai/claude-code")
		fmt.Println()
		fmt.Println("  Then run 'leo onboard' again.")
		return fmt.Errorf("claude CLI not found")
	}

	versionStr := claude.Version
	if versionStr == "" {
		versionStr = "installed"
	}
	prompt.Success.Printf("    claude CLI    ✓ %s\n", versionStr)
	fmt.Println()

	// 3. Run setup
	return setupInteractiveFn(reader)
}

// SmokeTest runs a quick claude invocation to verify it works.
func SmokeTest() error {
	cmd := execCommand("claude",
		"-p", "Reply with exactly: LEO_SMOKE_OK",
		"--max-turns", "1",
		"--output-format", "text",
	)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("claude smoke test failed: %w", err)
	}

	if !strings.Contains(string(output), "LEO_SMOKE_OK") {
		return fmt.Errorf("unexpected claude output: %s", string(output))
	}

	return nil
}
