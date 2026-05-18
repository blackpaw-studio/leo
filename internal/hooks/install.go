// Package hooks manages leo-owned entries inside Claude Code's
// .claude/settings.local.json file in a session's workspace.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const leoManagedKey = "_leo_managed"
const leoStopHookLabel = "task-report"
const leoStopCommand = "leo internal task-report"

// EnsureLeoStopHook idempotently merges the leo-managed Stop hook into
// <workspace>/.claude/settings.local.json. Preserves all non-leo entries.
// Atomic write via os.Rename. Refuses (returns error) if the existing file
// contains malformed JSON rather than clobber user data.
func EnsureLeoStopHook(workspace string) error {
	if workspace == "" {
		return errors.New("hooks.EnsureLeoStopHook: empty workspace")
	}
	dir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "settings.local.json")

	root := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(raw) > 0 {
			if jerr := json.Unmarshal(raw, &root); jerr != nil {
				return fmt.Errorf("parse %s: %w (refusing to overwrite)", path, jerr)
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		// start from empty
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	stops, _ := hooks["Stop"].([]any)
	pruned := stops[:0:0]
	for _, item := range stops {
		entry, ok := item.(map[string]any)
		if !ok {
			pruned = append(pruned, item)
			continue
		}
		if entry[leoManagedKey] == leoStopHookLabel {
			continue // drop leo-managed; we'll re-add below
		}
		pruned = append(pruned, item)
	}
	pruned = append(pruned, map[string]any{
		leoManagedKey: leoStopHookLabel,
		"command":     leoStopCommand,
	})
	hooks["Stop"] = pruned
	root["hooks"] = hooks

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Preserve existing file permissions when present; otherwise default to
	// user-only (0o600). settings.local.json can contain bearer tokens and
	// other sensitive material, so don't silently widen permissions.
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	// Best-effort cleanup if Rename fails (e.g., cross-device link on some
	// filesystems). No-op on success since Rename removes the source.
	defer os.Remove(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
