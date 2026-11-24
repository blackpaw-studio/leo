package credentials

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	securityBinary      = "/usr/bin/security"
	claudeKeychainBase  = "Claude Code-credentials"
	keychainItemMissing = 44 // errSecItemNotFound as `security` exits
	maxCredentialBytes  = 64 * 1024
	defaultKeychainWait = 5 * time.Second
)

// Runner runs one command without a shell and returns its stdout and exit
// code. A non-nil error means the command didn't run to completion (it
// couldn't start, or the context ended); a non-zero exit is not an error.
// Stdout holds a token, so a Runner must never log it.
type Runner func(ctx context.Context, name string, args ...string) (stdout []byte, exitCode int, err error)

// Credential is a resolved account token. It never leaves the process and
// is never formatted: the zero-value String hides the fields.
type Credential struct {
	Token     string
	AccountID string // Codex only; empty when the account has none
}

func (Credential) String() string { return "<credential redacted>" }

// TokenSource reads an account's token fresh on every call, from where its
// CLI keeps it. It never refreshes a token and never writes a file or a
// Keychain item: rotating a token would sign the CLI out.
type TokenSource struct {
	// Run executes the `security` CLI. Required when UseKeychain is set.
	Run Runner
	// ReadFile reads a credential file; nil reads from disk, capped at 64 KiB
	// and regular files only.
	ReadFile func(path string) ([]byte, error)
	// User is the Keychain account Claude Code stores its item under ($USER).
	User string
	// Home is the home dir; the bare ~/.claude uses the unsuffixed service.
	Home string
	// UseKeychain reads Claude's Keychain item before its file. Off where
	// there is no Keychain.
	UseKeychain bool
	// Timeout bounds one `security` call; zero means five seconds.
	Timeout time.Duration
}

// ServiceName is Claude Code's Keychain service for a config dir: the bare
// name for ~/.claude, else suffixed with the first 8 hex chars of the sha256
// of the cleaned absolute path.
func ServiceName(dir, home string) string {
	clean := filepath.Clean(dir)
	if clean == filepath.Join(home, ".claude") {
		return claudeKeychainBase
	}
	sum := sha256.Sum256([]byte(clean))
	return claudeKeychainBase + "-" + hex.EncodeToString(sum[:])[:8]
}

// Resolve returns the token for the account at dir. dir must already have
// been checked against Accounts. Errors never carry token bytes.
func (s TokenSource) Resolve(ctx context.Context, kind Kind, dir string) (Credential, *Error) {
	switch kind {
	case KindClaudeCode:
		return s.claude(ctx, dir)
	case KindCodex:
		return s.codex(dir)
	}
	return Credential{}, newError(ReasonInvalid, "unsupported account kind")
}

func (s TokenSource) claude(ctx context.Context, dir string) (Credential, *Error) {
	blob, err := s.claudeBlob(ctx, dir)
	if err != nil {
		return Credential{}, err
	}
	var stored struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(blob, &stored) != nil {
		return Credential{}, newError(ReasonInvalid, "the stored login is not readable")
	}
	token, ok := headerSafe(stored.ClaudeAiOauth.AccessToken)
	if !ok {
		return Credential{}, newError(ReasonInvalid, "the stored login has no usable token")
	}
	return Credential{Token: token}, nil
}

// claudeBlob reads the login JSON: the Keychain item first, the file only
// when the item doesn't exist. Any other Keychain failure is needs-
// authorization and does not fall back, since a stale file would mask the
// live token.
func (s TokenSource) claudeBlob(ctx context.Context, dir string) ([]byte, *Error) {
	if s.UseKeychain && s.Run != nil {
		wait := s.Timeout
		if wait <= 0 {
			wait = defaultKeychainWait
		}
		callCtx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		out, code, err := s.Run(callCtx, securityBinary,
			"find-generic-password", "-a", s.User, "-s", ServiceName(dir, s.Home), "-w")
		switch {
		case err != nil || (code != 0 && code != keychainItemMissing):
			return nil, newError(ReasonNeedsAuthorization, "the Keychain would need to ask for access")
		case code == 0:
			return bytes.TrimSpace(out), nil
		}
	}
	data, err := s.readFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return nil, newError(ReasonInvalid, "no login found for this account")
	}
	return data, nil
}

func (s TokenSource) codex(dir string) (Credential, *Error) {
	data, err := s.readFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		return Credential{}, newError(ReasonInvalid, "no login found for this account")
	}
	var stored struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &stored) != nil {
		return Credential{}, newError(ReasonInvalid, "the stored login is not readable")
	}
	token, ok := headerSafe(stored.Tokens.AccessToken)
	if !ok {
		return Credential{}, newError(ReasonInvalid, "the stored login has no usable token")
	}
	cred := Credential{Token: token}
	if stored.Tokens.AccountID != "" {
		id, ok := headerSafe(stored.Tokens.AccountID)
		if !ok {
			return Credential{}, newError(ReasonInvalid, "the stored account id is not usable")
		}
		cred.AccountID = id
	}
	return cred, nil
}

func (s TokenSource) readFile(path string) ([]byte, error) {
	if s.ReadFile != nil {
		return s.ReadFile(path)
	}
	return readCapped(path)
}

// readCapped reads a regular file of at most maxCredentialBytes. The check is
// on the opened descriptor, so a FIFO or device can't stall the read.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCredentialBytes {
		return nil, errors.New("file too large")
	}
	return data, nil
}

// headerSafe accepts a non-empty value of visible ASCII only, so it can't
// split a header.
func headerSafe(v string) (string, bool) {
	if v == "" {
		return "", false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x21 || v[i] > 0x7E {
			return "", false
		}
	}
	return v, true
}
