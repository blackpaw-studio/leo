package observe

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// defaultAutoCmdTitle names a command the daemon could not describe.
const defaultAutoCmdTitle = "Shell command"

// rawProgramToken is the only shape a program word may have before any path
// handling. Substitutions, redirections, quotes and braces fail it, so nothing
// but a plain (possibly path-qualified) name is ever reduced to a title.
var rawProgramToken = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// titlePrograms is the fixed set of programs a title may name. Any other
// program is "Shell command": a name outside this list could be a secret.
var titlePrograms = map[string]bool{
	"make": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "bun": true,
	"npx": true, "cargo": true, "swift": true, "xcodebuild": true, "xcrun": true,
	"docker": true, "git": true, "zig": true, "gradle": true, "mvn": true,
	"pytest": true, "python": true, "python3": true, "node": true, "rsync": true,
	"terraform": true, "nomad": true, "kubectl": true, "brew": true, "gh": true,
}

// safeVerb is the shape a sub-command verb must have to be considered.
var safeVerb = regexp.MustCompile(`^[a-z][a-z0-9:-]{0,24}$`)

// setupPrograms are shell builtins that prepare a command rather than being
// it, skipped when they lead a `cd dir && make` style line.
var setupPrograms = map[string]bool{"cd": true, "pushd": true, "export": true, "set": true, "source": true, "unset": true}

// wrapperPrograms run the command that follows them. They are skipped when
// picking the program; their own arguments are never emitted.
var wrapperPrograms = map[string]bool{"sudo": true, "env": true, "time": true, "nice": true, "nohup": true, "command": true}

// knownVerbs are the fixed sub-command verbs a title may append to a program.
// Anything else in that position (a target, a path, a secret) is dropped.
var knownVerbs = map[string][]string{
	"make":       {"all", "build", "test", "lint", "install", "check", "clean", "release"},
	"go":         {"build", "test", "run", "vet", "generate", "install", "mod"},
	"npm":        {"install", "test", "run", "ci", "build", "publish"},
	"pnpm":       {"install", "test", "run", "build", "publish"},
	"yarn":       {"install", "test", "run", "build", "publish"},
	"bun":        {"install", "test", "run", "build"},
	"cargo":      {"build", "test", "run", "check", "clippy", "bench", "publish"},
	"git":        {"clone", "fetch", "pull", "push", "rebase", "merge", "checkout", "submodule"},
	"xcodebuild": {"build", "test", "archive", "clean"},
	"docker":     {"build", "run", "compose", "pull", "push", "buildx"},
	"swift":      {"build", "test", "run", "package"},
	"zig":        {"build", "test", "run"},
	"gradle":     {"build", "test", "assemble", "check", "clean"},
	"mvn":        {"install", "test", "package", "verify", "clean", "compile"},
}

// cmdSeparatorAt is the length of the command separator at cmd[i] (0 if none).
func cmdSeparatorAt(cmd string, i int) int {
	switch {
	case strings.HasPrefix(cmd[i:], "&&"), strings.HasPrefix(cmd[i:], "||"):
		return 2
	case cmd[i] == ';', cmd[i] == '|', cmd[i] == '\n':
		return 1
	}
	return 0
}

// splitSegments splits a command line on unquoted separators. It reports false
// when it cannot be trusted: an unbalanced quote, or a quote ahead of the
// first separator (the quoted text may hide a secret in the program position).
func splitSegments(cmd string) ([]string, bool) {
	var (
		segments           []string
		quote              byte
		seenQuote, seenSep bool
		segStart           int
	)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			quote, seenQuote = c, true
		default:
			n := cmdSeparatorAt(cmd, i)
			if n == 0 {
				continue
			}
			if seenQuote && !seenSep {
				return nil, false
			}
			seenSep = true
			segments = append(segments, cmd[segStart:i])
			i += n - 1
			segStart = i + 1
		}
	}
	if quote != 0 {
		return nil, false
	}
	return append(segments, cmd[segStart:]), true
}

// autoCmdTitle is the title of an auto command task: the tool's description
// when it has one, else the program name (plus a fixed sub-command verb for
// known tools), never an argument or the command line (which can carry
// secrets).
func autoCmdTitle(description, command string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	segments, ok := splitSegments(command)
	if !ok {
		return defaultAutoCmdTitle
	}
	for _, segment := range segments {
		words := commandWords(segment)
		if len(words) == 0 || setupPrograms[words[0]] {
			continue
		}
		words, ok = unwrap(words)
		if !ok || len(words) == 0 {
			return defaultAutoCmdTitle
		}
		if !rawProgramToken.MatchString(words[0]) {
			return defaultAutoCmdTitle
		}
		program := path.Base(words[0])
		if !titlePrograms[program] {
			return defaultAutoCmdTitle
		}
		if verb := knownVerb(program, words[1:]); verb != "" {
			return program + " " + verb
		}
		return program
	}
	return defaultAutoCmdTitle
}

// unwrap drops leading wrapper programs and any env assignments after them,
// returning the words starting at the wrapped program. It reports false when a
// wrapper carries any flag (it might take a secret as its value) or when a
// wrapper word is not a plain program token.
func unwrap(words []string) ([]string, bool) {
	for len(words) > 0 && rawProgramToken.MatchString(words[0]) && wrapperPrograms[path.Base(words[0])] {
		words = words[1:]
		for len(words) > 0 && (strings.HasPrefix(words[0], "-") || isEnvAssignment(words[0])) {
			if strings.HasPrefix(words[0], "-") {
				return nil, false
			}
			words = words[1:]
		}
	}
	return words, true
}

// knownVerb is the first argument when it is one of program's fixed verbs.
func knownVerb(program string, args []string) string {
	if len(args) == 0 || !safeVerb.MatchString(args[0]) {
		return ""
	}
	if slices.Contains(knownVerbs[program], args[0]) {
		return args[0]
	}
	return ""
}

// commandWords are a segment's whitespace-separated words after any leading
// NAME=value environment assignments.
func commandWords(segment string) []string {
	words := strings.Fields(segment)
	for len(words) > 0 && isEnvAssignment(words[0]) {
		words = words[1:]
	}
	return words
}

func isEnvAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
