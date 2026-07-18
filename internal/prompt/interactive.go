package prompt

import (
	"io/fs"
	"os"
)

// stdinStat is a seam so tests can simulate different stdin kinds
// (terminal, pipe, file redirect) without a real TTY.
var stdinStat = os.Stdin.Stat

// IsInteractive reports whether stdin is a terminal. Prompts that guard
// consequential actions (e.g. restarting the daemon) must not fall back to
// their default answer when nothing is attached to answer them — a piped or
// closed stdin reads as an immediate empty line, which silently "accepts"
// the default. Callers should skip such prompts entirely when this returns
// false. A stat error is treated as non-interactive: the safe direction.
func IsInteractive() bool {
	info, err := stdinStat()
	if err != nil {
		return false
	}
	return info.Mode()&fs.ModeCharDevice != 0
}
