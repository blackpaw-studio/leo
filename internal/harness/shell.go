package harness

import "strings"

// ShellCommand joins an argv as POSIX-shell words, for harnesses that run a
// hook command through a shell. Safe words stay bare, preserving the
// documented command shape; everything else is single-quoted with embedded
// apostrophes written as '\”.
func ShellCommand(args []string) string {
	words := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\r'\"\\$`;&|<>()*?[]{}!") {
			words = append(words, arg)
			continue
		}
		words = append(words, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return strings.Join(words, " ")
}

// ShellWords splits a command string into its words, undoing single and
// double quotes and backslash escapes. It is the inverse of ShellCommand and
// does no expansion.
func ShellWords(command string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	escaped, inWord := false, false
	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	for _, r := range command {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	flush()
	return words
}
