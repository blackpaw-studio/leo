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
		{"suspended", StatusSuspended},
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
