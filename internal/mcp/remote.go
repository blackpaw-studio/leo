package mcp

import (
	"errors"
	"fmt"
	"strings"
)

// parseTarget splits a leo_send_message `to` argument. A bare name is a local
// agent and returned untouched, so existing callers behave exactly as before.
// "agent@host" addresses an agent on a peer host (a key in client.hosts); any
// other use of '@' is rejected rather than guessed at.
func parseTarget(to string) (agent, host string, remote bool, err error) {
	if !strings.Contains(to, "@") {
		return to, "", false, nil
	}
	if strings.IndexFunc(to, isSpace) >= 0 {
		return "", "", false, fmt.Errorf("target %q must not contain whitespace", to)
	}
	if strings.Count(to, "@") != 1 {
		return "", "", false, fmt.Errorf("target %q must have exactly one '@' (agent@host)", to)
	}
	agent, host, _ = strings.Cut(to, "@")
	switch {
	case agent == "":
		return "", "", false, errors.New("target is missing the agent name before '@' (agent@host)")
	case host == "":
		return "", "", false, errors.New("target is missing the host name after '@' (agent@host)")
	}
	return agent, host, true, nil
}

func isSpace(r rune) bool { return strings.ContainsRune(" \t\r\n\v\f", r) }
