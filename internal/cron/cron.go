package cron

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/env"
)

func markerStart(agentName string) string {
	return fmt.Sprintf("# === LEO:%s — DO NOT EDIT ===", agentName)
}

func markerEnd(agentName string) string {
	return fmt.Sprintf("# === END LEO:%s ===", agentName)
}

// Install writes cron entries for all enabled tasks.
func Install(cfg *config.Config, leoPath string) error {
	existing, err := readCrontab()
	if err != nil {
		return fmt.Errorf("reading crontab: %w", err)
	}

	// Remove existing leo block for this agent
	cleaned := removeBlock(existing, cfg.Agent.Name)

	// Build new block
	block := buildBlock(cfg, leoPath)

	// Append new block
	var newCrontab string
	if cleaned != "" && !strings.HasSuffix(cleaned, "\n") {
		cleaned += "\n"
	}
	newCrontab = cleaned + block

	return writeCrontab(newCrontab)
}

// Remove strips all leo-managed cron entries for this agent.
func Remove(cfg *config.Config) error {
	existing, err := readCrontab()
	if err != nil {
		return fmt.Errorf("reading crontab: %w", err)
	}

	cleaned := removeBlock(existing, cfg.Agent.Name)
	return writeCrontab(cleaned)
}

// List returns the leo-managed cron entries for this agent.
func List(cfg *config.Config) (string, error) {
	existing, err := readCrontab()
	if err != nil {
		return "", fmt.Errorf("reading crontab: %w", err)
	}

	return extractBlock(existing, cfg.Agent.Name), nil
}

// Installed returns true if leo-managed cron entries exist for this agent.
func Installed(agentName string) bool {
	existing, err := readCrontab()
	if err != nil {
		return false
	}
	return strings.Contains(existing, markerStart(agentName))
}

func buildBlock(cfg *config.Config, leoPath string) string {
	if leoPath == "" {
		leoPath = "leo"
	}

	var lines []string
	lines = append(lines, markerStart(cfg.Agent.Name))

	// Emit PATH so cron can find claude and other tools
	captured := env.Capture()
	if path, ok := captured["PATH"]; ok {
		lines = append(lines, fmt.Sprintf("PATH=%s", path))
	}

	cfgPath := cfg.Agent.Workspace + "/leo.yaml"

	for name, task := range cfg.Tasks {
		if !task.Enabled {
			continue
		}

		logPath := cfg.Agent.Workspace + "/state/" + name + ".log"
		line := fmt.Sprintf("%s %s run %s --config %s >> %s 2>&1",
			task.Schedule, leoPath, name, cfgPath, logPath)
		lines = append(lines, fmt.Sprintf("# leo:%s:%s", cfg.Agent.Name, name))
		lines = append(lines, line)
	}

	lines = append(lines, markerEnd(cfg.Agent.Name))
	return strings.Join(lines, "\n") + "\n"
}

func removeBlock(crontab, agentName string) string {
	start := markerStart(agentName)
	end := markerEnd(agentName)

	lines := strings.Split(crontab, "\n")
	var result []string
	inBlock := false

	for _, line := range lines {
		if strings.TrimSpace(line) == start {
			inBlock = true
			continue
		}
		if strings.TrimSpace(line) == end {
			inBlock = false
			continue
		}
		if !inBlock {
			result = append(result, line)
		}
	}

	// Trim trailing empty lines
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}

	if len(result) == 0 {
		return ""
	}

	return strings.Join(result, "\n") + "\n"
}

func extractBlock(crontab, agentName string) string {
	start := markerStart(agentName)
	end := markerEnd(agentName)

	lines := strings.Split(crontab, "\n")
	var result []string
	inBlock := false

	for _, line := range lines {
		if strings.TrimSpace(line) == start {
			inBlock = true
		}
		if inBlock {
			result = append(result, line)
		}
		if strings.TrimSpace(line) == end {
			break
		}
	}

	return strings.Join(result, "\n")
}

var readCrontab = defaultReadCrontab
var writeCrontab = defaultWriteCrontab

func ExportReadCrontab() func() (string, error)  { return readCrontab }
func SetReadCrontab(fn func() (string, error))    { readCrontab = fn }
func ExportWriteCrontab() func(string) error      { return writeCrontab }
func SetWriteCrontab(fn func(string) error)       { writeCrontab = fn }

const crontabTimeout = 10 * time.Second

func defaultReadCrontab() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), crontabTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "crontab", "-l")
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("crontab -l timed out after %s", crontabTimeout)
		}
		// Empty crontab returns error on some systems
		if exitErr, ok := err.(*exec.ExitError); ok {
			if strings.Contains(string(exitErr.Stderr), "no crontab") {
				return "", nil
			}
		}
		return "", err
	}
	return string(output), nil
}

func defaultWriteCrontab(content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), crontabTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("crontab - timed out after %s", crontabTimeout)
	}
	return err
}
