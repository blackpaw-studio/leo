package opencode

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// serverPasswordHexBytes yields a 32-hex-char password (16 random bytes,
// hex-encoded 2 chars per byte).
const serverPasswordHexBytes = 16

// ServerState is the persisted opencode-serve provisioning record for one
// tmux session: the port it listens on, the password gating access to it,
// and the model turns should use. Reused across restarts so the port stays
// stable (attach URLs and any in-flight references keep working) and a crash
// of `opencode serve` never re-provisions a fresh port out from under a
// stored session id.
type ServerState struct {
	Port     int    `json:"port"`
	Password string `json:"password"`
	Model    string `json:"model"`
}

// URL returns the server's base URL.
func (s ServerState) URL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.Port)
}

func serverStatePath(homePath, tmuxSession string) string {
	return filepath.Join(homePath, "state", "opencode", tmuxSession+".json")
}

// LoadServerState reads a previously-provisioned ServerState for tmuxSession.
func LoadServerState(homePath, tmuxSession string) (ServerState, error) {
	data, err := os.ReadFile(serverStatePath(homePath, tmuxSession))
	if err != nil {
		return ServerState{}, fmt.Errorf("opencode: reading server state for %s: %w", tmuxSession, err)
	}
	var s ServerState
	if err := json.Unmarshal(data, &s); err != nil {
		return ServerState{}, fmt.Errorf("opencode: parsing server state for %s: %w", tmuxSession, err)
	}
	return s, nil
}

// EnsureServerState reuses an existing on-disk ServerState for tmuxSession
// (port stability across restarts), or provisions a fresh one: a free
// localhost port (allocated via a throwaway listen-then-close, since
// `opencode serve --port 0` picks a port discoverable only via log-scraping)
// and a random 32-hex-char password.
func EnsureServerState(homePath, tmuxSession, model string) (ServerState, error) {
	if s, err := LoadServerState(homePath, tmuxSession); err == nil {
		return s, nil
	}

	port, err := freeLocalPort()
	if err != nil {
		return ServerState{}, fmt.Errorf("opencode: allocating server port: %w", err)
	}
	password, err := randomHex(serverPasswordHexBytes)
	if err != nil {
		return ServerState{}, fmt.Errorf("opencode: generating server password: %w", err)
	}
	state := ServerState{Port: port, Password: password, Model: model}

	dir := filepath.Dir(serverStatePath(homePath, tmuxSession))
	if err := os.MkdirAll(dir, 0750); err != nil {
		return ServerState{}, fmt.Errorf("opencode: creating server state dir %s: %w", dir, err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return ServerState{}, fmt.Errorf("opencode: marshaling server state: %w", err)
	}
	if err := os.WriteFile(serverStatePath(homePath, tmuxSession), data, 0600); err != nil {
		return ServerState{}, fmt.Errorf("opencode: writing server state: %w", err)
	}
	return state, nil
}

// freeLocalPort binds an ephemeral TCP port on 127.0.0.1, closes it
// immediately, and returns the port number opencode's serve should bind
// next. Inherently racy against a concurrent bind, but leo controls every
// process on this host that would compete for the port.
func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", l.Addr())
	}
	return addr.Port, nil
}

func randomHex(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
