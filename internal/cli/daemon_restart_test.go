package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRestartDaemonAndWait(t *testing.T) {
	restartErr := errors.New("kickstart failed")
	waitErr := errors.New("timed out")

	tests := []struct {
		name         string
		restartErr   error
		waitErr      error
		wantWaited   bool
		wantNotReady bool
		wantErr      error
	}{
		{name: "ready", wantWaited: true},
		{name: "restart fails: no wait, not a readiness warning", restartErr: restartErr, wantErr: restartErr},
		{name: "never ready: classified as not-ready", waitErr: waitErr, wantWaited: true, wantNotReady: true, wantErr: waitErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waited := false
			deps := daemonRestartDeps{
				restart: func(home string) error {
					if home != "/home/leo" {
						t.Errorf("restart home = %q", home)
					}
					return tt.restartErr
				},
				waitReady: func(_ context.Context, home string, timeout time.Duration) error {
					waited = true
					if home != "/home/leo" || timeout != daemonReadyTimeout {
						t.Errorf("waitReady(%q, %s)", home, timeout)
					}
					return tt.waitErr
				},
			}

			err := restartDaemonAndWait(context.Background(), "/home/leo", deps)

			if waited != tt.wantWaited {
				t.Errorf("waited = %v, want %v", waited, tt.wantWaited)
			}
			if got := errors.Is(err, errDaemonNotReady); got != tt.wantNotReady {
				t.Errorf("errors.Is(err, errDaemonNotReady) = %v, want %v (err: %v)", got, tt.wantNotReady, err)
			}
			if tt.wantErr == nil && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want wrapping %v", err, tt.wantErr)
			}
		})
	}
}
