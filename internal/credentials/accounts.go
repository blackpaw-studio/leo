package credentials

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Environment variables that point each kind's CLI at a non-default dir.
const (
	claudeDirVar = "CLAUDE_CONFIG_DIR"
	codexDirVar  = "CODEX_HOME"
)

type accountKey struct {
	kind Kind
	dir  string
}

// Accounts lists the accounts on this machine: the default ~/.claude and
// ~/.codex, then every literal CLAUDE_CONFIG_DIR / CODEX_HOME an environment
// names. A secret reference, an unset variable or a relative path is skipped:
// the list is an allowlist, so only a dir it can state exactly belongs on it.
// Environments are visited in name order and the first to name a dir labels
// it; a dir without an environment is labelled by its basename. The output
// order is stable and holds no token or token metadata.
func Accounts(cfg *config.Config, home string) []Account {
	x := config.Expander{
		Home: func() (string, error) { return home, nil },
		Lookup: func(name string) (string, bool) {
			if name == "HOME" {
				return home, true
			}
			return os.LookupEnv(name)
		},
	}

	var out []Account
	index := map[accountKey]int{}
	named := map[accountKey]bool{} // labelled by an environment already
	add := func(kind Kind, dir, envName string) {
		key := accountKey{kind, dir}
		i, seen := index[key]
		if !seen {
			i = len(out)
			index[key] = i
			out = append(out, Account{Kind: kind, Dir: dir, Label: filepath.Base(dir)})
		}
		if envName != "" && !named[key] {
			named[key] = true
			out[i].Label = envName
		}
	}

	add(KindClaudeCode, filepath.Join(home, ".claude"), "")
	add(KindCodex, filepath.Join(home, ".codex"), "")

	if cfg == nil {
		return out
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Environments)) {
		env := cfg.Environments[name]
		for _, v := range []struct {
			kind   Kind
			envVar string
		}{{KindClaudeCode, claudeDirVar}, {KindCodex, codexDirVar}} {
			if dir, ok := literalDir(env, v.envVar, x); ok {
				add(v.kind, dir, name)
			}
		}
	}
	return out
}

// literalDir reads env[name] as an absolute directory: literal values only,
// expanded and cleaned.
func literalDir(env config.EnvMap, name string, x config.Expander) (string, bool) {
	raw, ok := env[name].AsLiteral()
	if _, isRef := env[name].Ref(); !ok || isRef || strings.TrimSpace(raw) == "" {
		return "", false
	}
	expanded, err := x.Expand(raw)
	if err != nil || !filepath.IsAbs(expanded) {
		return "", false
	}
	return filepath.Clean(expanded), true
}

// Find reports whether kind and dir name a listed account. The dir is
// compared after cleaning, so "/a/../b" matches "/b" but nothing outside the
// list ever does.
func Find(accounts []Account, kind Kind, dir string) (Account, bool) {
	if !filepath.IsAbs(dir) {
		return Account{}, false
	}
	clean := filepath.Clean(dir)
	for _, a := range accounts {
		if a.Kind == kind && a.Dir == clean {
			return a, true
		}
	}
	return Account{}, false
}
