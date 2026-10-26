package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// securityGuardPlugin names Claude Code's built-in cc-plugin-sec-default mod.
// Seated, it passes mod-API events on past person-installed mods; the leo
// bridge takes everything it needs from Claude command hooks, which the guard
// does not touch, so nothing breaks either way (see
// docs/specs/2026-10-26-bridge-command-hooks.md).
const securityGuardPlugin = "cc-plugin-sec-default"

// guardNothingBreaks ends every guard line: it is informational only.
const guardNothingBreaks = "nothing breaks either way: leo's bridge features run on command hooks"

// managedClaude is what Claude Code's managed configuration says that decides
// whether the guard seats. PrependPlugins is nil when the key is unset.
type managedClaude struct {
	HasSettings    bool
	HasMCP         bool
	PrependPlugins *[]string
}

// guardDir is one Claude config dir leo launches agents with and the account
// logged into it (oauthAccount.organizationType; "" when none).
type guardDir struct {
	Label   string
	OrgType string
}

// guardSeatLines says, per config dir, whether the guard will seat: when
// managed prependPlugins is set it decides alone; when unset the guard seats
// if managed settings or a managed MCP file exist, or the account is a
// team/enterprise one. Every line is informational.
func guardSeatLines(m managedClaude, dirs []guardDir) []doctorLine {
	out := make([]doctorLine, 0, len(dirs))
	for _, d := range dirs {
		seats, why := guardSeats(m, d)
		verdict := "will not seat"
		if seats {
			verdict = "will seat"
		}
		out = append(out, doctorLine{"info", fmt.Sprintf("Plugin guard:   %s: cc-plugin-sec-default %s (%s); %s", d.Label, verdict, why, guardNothingBreaks)})
	}
	return out
}

func guardSeats(m managedClaude, d guardDir) (bool, string) {
	if m.PrependPlugins != nil {
		if slices.ContainsFunc(*m.PrependPlugins, func(p string) bool { return strings.Contains(p, securityGuardPlugin) }) {
			return true, "managed prependPlugins lists it"
		}
		return false, "managed prependPlugins does not list it"
	}
	switch {
	case m.HasSettings:
		return true, "managed settings exist"
	case m.HasMCP:
		return true, "a managed MCP file exists"
	case d.OrgType == "claude_team" || d.OrgType == "claude_enterprise":
		return true, "account is " + d.OrgType
	case d.OrgType == "":
		return false, "no managed config, no account logged in"
	}
	return false, "no managed config, account is " + d.OrgType
}

// managedClaudeDirs are where Claude Code reads managed configuration.
func managedClaudeDirs() []string {
	if runtime.GOOS == "darwin" {
		return []string{"/Library/Application Support/ClaudeCode"}
	}
	return []string{"/etc/claude-code"}
}

// probeManagedClaude reads the managed configuration under dirs: whether
// settings (managed-settings.json or managed-settings.d drop-ins) or a
// managed MCP file exist, and prependPlugins when any settings file sets it
// (later files win, drop-ins in name order). A settings file that cannot be
// parsed still counts as present.
func probeManagedClaude(dirs []string) managedClaude {
	var m managedClaude
	for _, dir := range dirs {
		files := []string{filepath.Join(dir, "managed-settings.json")}
		dropIns, _ := filepath.Glob(filepath.Join(dir, "managed-settings.d", "*.json"))
		sort.Strings(dropIns)
		for _, file := range append(files, dropIns...) {
			raw, err := os.ReadFile(file) // #nosec G304 -- Claude Code's managed config path
			if err != nil {
				continue
			}
			m.HasSettings = true
			var settings struct {
				PrependPlugins *[]string `json:"prependPlugins"`
			}
			if json.Unmarshal(raw, &settings) == nil && settings.PrependPlugins != nil {
				m.PrependPlugins = settings.PrependPlugins
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "managed-mcp.json")); err == nil {
			m.HasMCP = true
		}
	}
	return m
}

// readOrgType is the organization type of the account logged into the
// claude state file at path, "" when unreadable or logged out.
func readOrgType(path string) string {
	raw, err := os.ReadFile(path) // #nosec G304 -- Claude's per-account state file
	if err != nil {
		return ""
	}
	var state struct {
		OAuthAccount struct {
			OrganizationType string `json:"organizationType"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return ""
	}
	return state.OAuthAccount.OrganizationType
}

// guardDirsFor lists the Claude config dirs leo launches with: the default
// account and each environment that sets CLAUDE_CONFIG_DIR.
func guardDirsFor(cfg *config.Config) []guardDir {
	var dirs []guardDir
	if path, err := claudeharness.StateFile(map[string]string{}); err == nil {
		dirs = append(dirs, guardDir{Label: "default account", OrgType: readOrgType(path)})
	}
	names := make([]string, 0, len(cfg.Environments))
	for name := range cfg.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env := cfg.Environments[name]
		if env["CLAUDE_CONFIG_DIR"] == "" {
			continue
		}
		if path, err := claudeharness.StateFile(env); err == nil {
			dirs = append(dirs, guardDir{Label: "environment " + name, OrgType: readOrgType(path)})
		}
	}
	return dirs
}

// reportGuardSeat prints whether the security guard will seat for each
// Claude config dir leo uses.
func reportGuardSeat(cfg *config.Config) {
	for _, l := range guardSeatLines(probeManagedClaude(managedClaudeDirs()), guardDirsFor(cfg)) {
		info.Println(l.Text)
	}
}
