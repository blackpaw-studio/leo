package web

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// errPromptProtected is the 422 for a prompt_file that is leo's own config or
// state: a task whose workspace contains leo home must not be able to read or
// overwrite them through the prompt routes.
func errPromptProtected() *ConfigError {
	return errInvalid("prompt_file", "prompt_file may not be leo.yaml or anything under leo's state directory")
}

// protectedPrompt reports whether abs, after symlinks, is the active config
// file or lies under the state dir.
func (c *ConfigService) protectedPrompt(cfg *config.Config, abs string) bool {
	target := resolveThroughSymlinks(abs)
	if cfgPath := resolveThroughSymlinks(c.configPath); target == cfgPath {
		return true
	}
	if sameFile(abs, c.configPath) {
		return true
	}
	state := resolveThroughSymlinks(cfg.StatePath())
	return target == state || strings.HasPrefix(target, state+string(filepath.Separator))
}

// resolveThroughSymlinks returns p absolute with every symlink resolved. A
// path that does not exist yet (a prompt about to be created) resolves its
// longest existing ancestor and re-appends the rest, so a symlinked directory
// is still seen through.
func resolveThroughSymlinks(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	var rest []string
	for cur := abs; ; cur = filepath.Dir(cur) {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, rest[i])
			}
			return resolved
		}
		if filepath.Dir(cur) == cur {
			return abs
		}
		rest = append(rest, filepath.Base(cur))
	}
}

// sameFile catches a hard link to the config, which path comparison cannot.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// resolveGuardedPrompt resolves task's prompt_file inside its workspace and
// refuses leo's own files.
func (c *ConfigService) resolveGuardedPrompt(cfg *config.Config, workspace, file string) (string, error) {
	abs, err := config.ResolvePromptPath(workspace, file)
	if err != nil {
		return "", errInvalid("prompt_file", err.Error())
	}
	if c.protectedPrompt(cfg, abs) {
		return "", errPromptProtected()
	}
	return abs, nil
}
