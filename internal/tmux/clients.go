package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HasAttachedClient reports whether sessionName currently has at least one
// tmux client attached. The startup-dialog auto-dismisser uses this to stay
// out of an attended session's way: dismissal exists only to unblock
// UNATTENDED sessions, and a human who has attached and opened an
// interactive menu (e.g. Claude's /mcp picker, which renders the same
// confirm/cancel footer as a blocking startup dialog) must be left to
// dismiss it themselves.
//
// Errors (no such session, tmux not reachable, etc.) report false — fail
// open to dismissal, preserving the existing unattended behavior rather than
// silently going quiet on a session we can't inspect.
func HasAttachedClient(ctx context.Context, tmuxPath, sessionName string) bool {
	out, err := execCommand(ctx, tmuxPath, Args("list-clients", "-t", Target(sessionName))...).Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}

// Client is one tmux client attached to a session.
type Client struct {
	// PID is the client process (#{client_pid}).
	PID int
	// Created is when the client attached (#{client_created}); tmux reports
	// whole seconds.
	Created time.Time
}

// ListClients returns the clients attached to session. session is passed to
// tmux verbatim, so a session id ($3) works as well as a name. Lines that do
// not parse are skipped; a failing tmux is an error, so a caller can tell "no
// clients" from "could not look".
func ListClients(ctx context.Context, tmuxPath, session string) ([]Client, error) {
	out, err := execCommand(ctx, tmuxPath, Args("list-clients", "-t", session, "-F", "#{client_pid} #{client_created}")...).Output()
	if err != nil {
		return nil, fmt.Errorf("listing clients of %s: %w", session, err)
	}
	var clients []Client
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(f[0])
		created, createdErr := strconv.ParseInt(f[1], 10, 64)
		if pidErr != nil || createdErr != nil {
			continue
		}
		clients = append(clients, Client{PID: pid, Created: time.Unix(created, 0)})
	}
	return clients, nil
}
