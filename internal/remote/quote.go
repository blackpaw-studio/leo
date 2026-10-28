package remote

import "strings"

// LeoWord quotes a remote leo path for the remote login shell. A leading
// $HOME/ or ~/ is the one thing left for that shell to expand
// ("$HOME"/'rest'); everything else is single-quoted whole, so a path with a
// space, a quote or $() reaches the remote exec as one literal word.
func LeoWord(path string) string {
	for _, prefix := range []string{"$HOME/", "~/"} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			return `"$HOME"/` + Quote(rest)
		}
	}
	return Quote(path)
}

// Quote wraps a value in single quotes, escaping any embedded single quotes,
// so a shell (a remote login shell, or `/bin/sh -c` behind tmux
// display-popup) reads it back as one literal word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
