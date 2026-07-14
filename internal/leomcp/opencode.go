package leomcp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
)

// openCodeManagedBeginMarker and openCodeManagedEndMarker delimit the block
// of ~/.config/opencode/AGENTS.md that Leo owns. opencode merges its global
// AGENTS.md with any project-level AGENTS.md rather than overwriting either,
// so this file may also contain content the user wrote by hand; only the
// text between the markers is ever touched.
const (
	openCodeManagedBeginMarker = "<!-- BEGIN LEO (managed) -->"
	openCodeManagedEndMarker   = "<!-- END LEO (managed) -->"
)

// openCodeAgentsPath returns the path to opencode's global AGENTS.md.
// opencode follows the XDG base-directory convention on every platform (not
// just Unix), so this honors $XDG_CONFIG_HOME when set rather than relying
// on os.UserConfigDir, which resolves to a Darwin/Windows-specific directory
// unrelated to opencode's actual config location.
func openCodeAgentsPath() (string, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home dir: %w", err)
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "opencode", "AGENTS.md"), nil
}

// EnsureOpenCodeContext writes Leo's harness-neutral nudge into opencode's
// global AGENTS.md, since opencode (unlike claude/codex) has no
// per-invocation system-prompt flag. The nudge lives in a managed block
// delimited by openCodeManagedBeginMarker/openCodeManagedEndMarker so any
// user-authored content in the file (opencode merges global + project
// AGENTS.md; it never overwrites) is preserved.
//
// Idempotent: no write occurs if the file already contains the current
// managed block. Returns nil without writing if LeoNudge(cfg) is empty.
func EnsureOpenCodeContext(cfg *config.Config) error {
	nudge := LeoNudge(cfg)
	if nudge == "" {
		return nil
	}

	path, err := openCodeAgentsPath()
	if err != nil {
		return err
	}

	managedBlock := openCodeManagedBeginMarker + "\n" + nudge + "\n" + openCodeManagedEndMarker

	existing, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read %s: %w", path, readErr)
	}

	next := mergeManagedBlock(string(existing), managedBlock)
	if readErr == nil && next == string(existing) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomic(path, []byte(next), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// mergeManagedBlock returns existing content with managedBlock spliced in:
// replacing the region between the markers if present, or appending
// managedBlock (preceded by a blank line when existing is non-empty)
// otherwise.
func mergeManagedBlock(existing, managedBlock string) string {
	beginIdx := indexOf(existing, openCodeManagedBeginMarker)
	endIdx := indexOf(existing, openCodeManagedEndMarker)
	if beginIdx >= 0 && endIdx >= 0 && endIdx > beginIdx {
		endIdx += len(openCodeManagedEndMarker)
		return existing[:beginIdx] + managedBlock + existing[endIdx:]
	}

	if existing == "" {
		return managedBlock
	}

	trimmed := existing
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == '\r') {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed + "\n\n" + managedBlock
}

func indexOf(haystack, needle string) int {
	return bytes.Index([]byte(haystack), []byte(needle))
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so readers never observe a partially-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".leo-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
