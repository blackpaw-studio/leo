package observe

import (
	"path"
	"regexp"
	"strings"
)

// defaultAutoCmdTitle names a command the daemon could not describe.
const defaultAutoCmdTitle = "Shell command"

// safeCmdWord is what may survive from a command line into a title: a plain
// program or sub-command name. Anything else (flags, URLs, quoted strings,
// substitutions) can carry a secret and is dropped.
var safeCmdWord = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// setupPrograms are shell builtins that prepare a command rather than being
// it, skipped when they lead a `cd dir && make` style line.
var setupPrograms = map[string]bool{"cd": true, "pushd": true, "export": true, "set": true, "source": true, "unset": true}

// cmdSegmentSeparators split a command line into simple commands.
var cmdSegmentSeparators = regexp.MustCompile(`&&|\|\||;|\||\n`)

// autoCmdTitle is the title of an auto command task: the tool's description
// when it has one, else the program and its first argument, never the whole
// command line (which can carry secrets).
func autoCmdTitle(description, command string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	for _, segment := range cmdSegmentSeparators.Split(command, -1) {
		words := commandWords(segment)
		if len(words) == 0 || setupPrograms[words[0]] {
			continue
		}
		program := path.Base(words[0])
		if !safeCmdWord.MatchString(program) {
			return defaultAutoCmdTitle
		}
		if len(words) > 1 && !strings.HasPrefix(words[1], "-") && safeCmdWord.MatchString(words[1]) {
			return program + " " + words[1]
		}
		return program
	}
	return defaultAutoCmdTitle
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
