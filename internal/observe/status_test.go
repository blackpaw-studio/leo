package observe

import "testing"

func TestMapStatus(t *testing.T) {
	cases := []struct {
		raw  string
		want Status
	}{
		{"running", StatusRunning},
		{"starting", StatusStarting},
		{"restarting", StatusStarting},
		{"stopped", StatusStopped},
		{"zombie", StatusStopped},
		{"", StatusStopped},
	}
	for _, tc := range cases {
		if got := MapStatus(tc.raw); got != tc.want {
			t.Errorf("MapStatus(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestAgentDormancy locks in the invariant this function exists to enforce:
// WakeOnMessage can only ever be true alongside StatusStopped. A caller that
// passes wakeOnMessage=true for a raw status that doesn't map to "stopped"
// must get it silently dropped, not forwarded — the same discipline the
// internal Stopped/WakeOnMessage pair already has (WakeOnMessage is only
// meaningful when Stopped is true).
func TestAgentDormancy(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		wakeOnMessage bool
		wantStatus    Status
		wantWakeOnMsg bool
	}{
		{"running agent, wake flag stale/irrelevant", "running", true, StatusRunning, false},
		{"starting agent, wake flag stale/irrelevant", "starting", true, StatusStarting, false},
		{"idle-swept dormant agent wakes on message", "stopped", true, StatusStopped, true},
		{"manually stopped agent stays dormant", "stopped", false, StatusStopped, false},
		{"unrecognized raw status folds to stopped, wake forwarded", "zombie", true, StatusStopped, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotWake := AgentDormancy(tc.raw, tc.wakeOnMessage)
			if gotStatus != tc.wantStatus || gotWake != tc.wantWakeOnMsg {
				t.Errorf("AgentDormancy(%q, %v) = (%q, %v), want (%q, %v)",
					tc.raw, tc.wakeOnMessage, gotStatus, gotWake, tc.wantStatus, tc.wantWakeOnMsg)
			}
		})
	}
}
