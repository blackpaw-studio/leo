package daemon

import (
	"context"
	"net/http"
)

// MoveVerb posts a destination verb's manifest to the daemon at workDir and
// returns the HTTP status and body. There is no overall timeout: a commit
// runs post_receive hooks and waits out the start confirmation.
func MoveVerb(ctx context.Context, workDir, verb string, body []byte) (int, []byte, error) {
	return sendRaw(ctx, newUnixClientNoTimeout(SockPath(workDir)), http.MethodPost, "http://daemon/agents/move/"+verb, body)
}
