package config

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

// LookupFunc reports the value of an environment variable and whether it is
// set, like os.LookupEnv.
type LookupFunc func(name string) (string, bool)

// Expander expands "$VAR", "${VAR}", a leading "~" or "~/", and "$$" in
// environment values. The zero value reads the launching process's
// environment (os.LookupEnv) and home directory (os.UserHomeDir); tests
// inject both.
//
// Config structs hold raw values, so a web-UI save writes "${HOME}" back to
// leo.yaml untouched. Expansion happens only when an environment is resolved
// for a launch.
type Expander struct {
	Lookup LookupFunc
	Home   func() (string, error)
}

// UnsetVariableError reports a "$VAR" reference whose variable is not set.
// Scope and Key locate the value (e.g. "environments.acct-b" and
// "CLAUDE_CONFIG_DIR"); they are empty until a resolver fills them in.
type UnsetVariableError struct {
	Scope, Key, Var string
}

func (e *UnsetVariableError) Error() string {
	where := e.Key
	if e.Scope != "" {
		where = e.Scope + "." + e.Key
	}
	if where == "" {
		return fmt.Sprintf("variable $%s is not set", e.Var)
	}
	return fmt.Sprintf("%s: variable $%s is not set", where, e.Var)
}

func (x Expander) lookup(name string) (string, bool) {
	if x.Lookup != nil {
		return x.Lookup(name)
	}
	return os.LookupEnv(name)
}

func (x Expander) home() (string, error) {
	if x.Home != nil {
		return x.Home()
	}
	return os.UserHomeDir()
}

// Expand returns s with variables and a leading tilde expanded. An unset
// variable is an error (*UnsetVariableError): silently substituting "" would
// turn "$HOME/x" into "/x". A "$" not followed by a name or "{" stays
// literal; "~user" is not expanded.
func (x Expander) Expand(s string) (string, error) {
	var b strings.Builder
	rest := s
	if s == "~" || strings.HasPrefix(s, "~/") {
		home, err := x.home()
		if err != nil {
			return "", fmt.Errorf("expanding ~: %w", err)
		}
		b.WriteString(home)
		rest = s[1:]
	}
	for i := 0; i < len(rest); {
		c := rest[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		name, width, err := parseVarRef(rest[i:])
		if err != nil {
			return "", err
		}
		switch {
		case width == 0: // lone "$": literal
			b.WriteByte('$')
			i++
		case name == "$": // "$$" escape
			b.WriteByte('$')
			i += width
		default:
			v, ok := x.lookup(name)
			if !ok {
				return "", &UnsetVariableError{Var: name}
			}
			b.WriteString(v)
			i += width
		}
	}
	return b.String(), nil
}

// parseVarRef parses the reference at the start of s (s[0] == '$'). It
// returns the variable name and the bytes consumed; width 0 means a literal
// "$", and name "$" with width 2 means the "$$" escape.
func parseVarRef(s string) (name string, width int, err error) {
	if len(s) < 2 {
		return "", 0, nil
	}
	switch {
	case s[1] == '$':
		return "$", 2, nil
	case s[1] == '{':
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return "", 0, fmt.Errorf("unterminated ${ in %q", s)
		}
		name = s[2:end]
		if !isVarName(name) {
			return "", 0, fmt.Errorf("invalid variable name %q in %q", name, s[:end+1])
		}
		return name, end + 1, nil
	case isNameStart(s[1]):
		end := 2
		for end < len(s) && isNameChar(s[end]) {
			end++
		}
		return s[1:end], end, nil
	}
	return "", 0, nil
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

func isVarName(s string) bool {
	if s == "" || !isNameStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isNameChar(s[i]) {
			return false
		}
	}
	return true
}

// expandMap expands every value of env, naming scope and key in any error. It
// returns a new map; env is untouched.
func (x Expander) expandMap(scope string, env map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		expanded, err := x.Expand(env[k])
		if err != nil {
			return nil, scoped(err, scope, k)
		}
		out[k] = expanded
	}
	return out, nil
}

func scoped(err error, scope, key string) error {
	if unset, ok := err.(*UnsetVariableError); ok {
		return &UnsetVariableError{Scope: scope, Key: key, Var: unset.Var}
	}
	return fmt.Errorf("%s.%s: %w", scope, key, err)
}
