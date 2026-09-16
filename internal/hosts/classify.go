package hosts

import "strings"

func ClassifySSHStderr(stderr string) *HostError {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	msg := strings.Join(lines, "\n")
	code := "ssh_failed"
	switch {
	case strings.Contains(stderr, "Permission denied"):
		code = "ssh_auth_required"
	case strings.Contains(stderr, "Host key verification failed"):
		code = "ssh_host_key_unknown"
	case strings.Contains(stderr, "Connection refused"), strings.Contains(stderr, "timed out"), strings.Contains(stderr, "No route"):
		code = "ssh_unreachable"
	}
	return &HostError{Code: code, Message: msg}
}
