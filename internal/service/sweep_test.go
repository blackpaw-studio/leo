package service

import (
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestShouldSuspend(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	idle := 30 * time.Minute

	cases := []struct {
		name string
		act  tmux.SessionActivity
		idle time.Duration
		want bool
	}{
		{"idle past threshold, detached", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-31 * time.Minute)}, idle, true},
		{"idle under threshold", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-29 * time.Minute)}, idle, false},
		{"attached blocks suspend", tmux.SessionActivity{Attached: 1, LastActivity: now.Add(-2 * time.Hour)}, idle, false},
		{"disabled interval", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-2 * time.Hour)}, 0, false},
		{"exactly at threshold suspends", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-30 * time.Minute)}, idle, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldSuspend(now, c.act, c.idle); got != c.want {
				t.Fatalf("shouldSuspend = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseIdle(t *testing.T) {
	if parseIdle("") != 0 || parseIdle("bad") != 0 || parseIdle("-5m") != 0 {
		t.Fatal("invalid/empty/negative durations must parse to 0")
	}
	if parseIdle("24h") != 24*time.Hour {
		t.Fatal("24h should parse")
	}
}
