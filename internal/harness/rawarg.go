package harness

import "strings"

// rawArgSentinel marks an argv element that must be emitted into a shell
// command line verbatim, bypassing the per-word quoting every other argv
// element gets. It exists so a shell command-substitution word (like
// `"$(cat '/path')"`) can ride inside an ordinary []string argv — persisted
// end-to-end through agentstore, a restart, and a crash-loop respawn —
// without a later per-word shell-quote pass (which wraps everything in
// single quotes) neutralizing the substitution.
//
// A NUL byte can never occur in a legitimate CLI argument — exec argv
// strings are NUL-terminated at the OS level — so this prefix can't collide
// with real argv content synthesized elsewhere.
const rawArgSentinel = "\x00leo-raw-argv\x00"

// RawArg wraps word so a per-word shell-quoting pass (see SplitRawArg)
// emits it unquoted instead.
func RawArg(word string) string {
	return rawArgSentinel + word
}

// SplitRawArg reports whether arg was wrapped by RawArg, returning the
// unwrapped word. Callers building a shell command line from an argv slice
// should emit word verbatim when raw is true, and shell-quote arg normally
// otherwise.
func SplitRawArg(arg string) (word string, raw bool) {
	if strings.HasPrefix(arg, rawArgSentinel) {
		return strings.TrimPrefix(arg, rawArgSentinel), true
	}
	return arg, false
}
