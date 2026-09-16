package hosts

import (
	"errors"
	"time"
)

var ErrClosed = errors.New("host hub closed")

type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateError        State = "error"
)

type Row struct {
	Name        string     `json:"name"`
	Local       bool       `json:"local"`
	Default     bool       `json:"default"`
	SSH         string     `json:"ssh,omitempty"`
	State       State      `json:"state"`
	Error       string     `json:"error,omitempty"`
	Code        string     `json:"code,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

type HostError struct{ Code, Message string }

func (e *HostError) Error() string { return e.Message }
