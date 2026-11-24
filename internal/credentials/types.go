// Package credentials lets the daemon act as a locally configured Claude or
// Codex account for a remote caller: it lists the accounts this machine
// knows, resolves their tokens and performs bound HTTPS requests with them.
// Tokens never leave the process: they are not returned, logged or put in
// errors.
package credentials

// Kind names a supported account kind. The values are wire constants shared
// with the Mac app.
type Kind string

const (
	KindClaudeCode Kind = "claude-code"
	KindCodex      Kind = "codex"
)

// BoundHost is the only host a kind's token may be sent to.
func (k Kind) BoundHost() (string, bool) {
	switch k {
	case KindClaudeCode:
		return "api.anthropic.com", true
	case KindCodex:
		return "chatgpt.com", true
	}
	return "", false
}

// Account is one account this machine can act as. It carries no token and no
// token metadata.
type Account struct {
	Kind  Kind   `json:"kind"`
	Dir   string `json:"dir"`
	Label string `json:"label"`
}

// Failure reasons on the wire. credentialMachineUnavailable is produced by
// the app only.
const (
	ReasonHostMismatch       = "credentialHostMismatch"
	ReasonHeaderConflict     = "credentialHeaderConflict"
	ReasonNeedsAuthorization = "credentialNeedsAuthorization"
	ReasonInvalid            = "credentialInvalid"
	ReasonInvalidRequest     = "invalidRequest"
	ReasonBodyTooLarge       = "bodyTooLarge"
	ReasonNetworkError       = "networkError"
)

// Error is a failure with a wire reason. Its message never holds token
// bytes.
type Error struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Reason + ": " + e.Message }

func newError(reason, message string) *Error { return &Error{Reason: reason, Message: message} }
