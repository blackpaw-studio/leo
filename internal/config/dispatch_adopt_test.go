package config

import (
	"strings"
	"testing"
	"time"
)

func TestDispatchAdoptTimeout(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", 2 * time.Minute},
		{"45s", 45 * time.Second},
		{"1h", time.Hour},
	} {
		cfg := &Config{}
		cfg.Defaults.Dispatch.AdoptTimeout = tc.raw
		if err := cfg.Validate(); err != nil {
			t.Fatalf("adopt_timeout %q: %v", tc.raw, err)
		}
		if got := cfg.DispatchAdoptTimeout(); got != tc.want {
			t.Fatalf("adopt_timeout %q = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestDispatchAdoptTimeoutRejectsUnusable(t *testing.T) {
	for _, bad := range []string{"0", "-1s", "2h", "x"} {
		cfg := &Config{}
		cfg.Defaults.Dispatch.AdoptTimeout = bad
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("adopt_timeout %q accepted", bad)
		}
		if !strings.Contains(err.Error(), "defaults.dispatch.adopt_timeout") {
			t.Fatalf("adopt_timeout %q: error %q does not name the field", bad, err)
		}
	}
}
