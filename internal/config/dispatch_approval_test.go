package config

import (
	"testing"
	"time"
)

func TestDispatchApprovalTimeout(t *testing.T) {
	cfg := &Config{}
	if got := cfg.DispatchApprovalTimeout(); got != 30*time.Minute {
		t.Fatalf("default = %s, want 30m", got)
	}
	cfg.Defaults.Dispatch.ApprovalTimeout = "5m"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.DispatchApprovalTimeout(); got != 5*time.Minute {
		t.Fatalf("configured = %s, want 5m", got)
	}
	for _, bad := range []string{"soon", "0s", "-1m", "25h"} {
		cfg.Defaults.Dispatch.ApprovalTimeout = bad
		if cfg.Validate() == nil {
			t.Fatalf("approval_timeout %q accepted", bad)
		}
	}
}
