package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

func doctorBridgeDeps(version string, probeErr error, agents []agent.Record, daemonUp bool) bridgeDoctorDeps {
	return bridgeDoctorDeps{
		lookClaude: func() (string, error) { return "/opt/bin/claude", nil },
		probe: func(context.Context, string) (string, error) {
			return version + " (Claude Code)", probeErr
		},
		daemonUp:   func(string) bool { return daemonUp },
		listAgents: func(context.Context, string) ([]agent.Record, error) { return agents, nil },
	}
}

func TestReportBridge(t *testing.T) {
	agents := []agent.Record{
		{Name: "alpha", Status: "running", Bridge: agent.BridgeConnected},
		{Name: "beta", Status: "running", Bridge: agent.BridgeAbsent},
		{Name: "codex-one", Status: "running"},
	}
	cases := []struct {
		name     string
		deps     bridgeDoctorDeps
		want     []string
		dontWant []string
	}{
		{
			name:     "tested claude, agents listed",
			deps:     doctorBridgeDeps("2.1.289", nil, agents, true),
			want:     []string{"2.1.289", "leo bridge: on", "alpha", "connected", "beta", "absent"},
			dontWant: []string{"codex-one", "newer than"},
		},
		{
			name: "claude newer than tested warns",
			deps: doctorBridgeDeps("2.1.290", nil, nil, true),
			want: []string{"2.1.290", "newer than 2.1.289"},
		},
		{
			name:     "claude too old for mods",
			deps:     doctorBridgeDeps("2.1.286", nil, nil, true),
			want:     []string{"2.1.286", "leo bridge: off", "2.1.287"},
			dontWant: []string{"newer than"},
		},
		{
			name: "unreadable claude",
			deps: doctorBridgeDeps("", errors.New("exec: boom"), nil, true),
			want: []string{"leo bridge: off", "boom"},
		},
		{
			name:     "daemon down lists no agents",
			deps:     doctorBridgeDeps("2.1.289", nil, agents, false),
			want:     []string{"2.1.289"},
			dontWant: []string{"alpha"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureColorOutput(t, func() { reportBridge(context.Background(), "/home", tc.deps) })
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, w := range tc.dontWant {
				if strings.Contains(out, w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestReportBridgeWithoutClaude(t *testing.T) {
	deps := doctorBridgeDeps("2.1.289", nil, nil, false)
	deps.lookClaude = func() (string, error) { return "", errors.New("not in PATH") }
	out := captureColorOutput(t, func() { reportBridge(context.Background(), "/home", deps) })
	if !strings.Contains(out, "not found") {
		t.Fatalf("output = %q", out)
	}
}
