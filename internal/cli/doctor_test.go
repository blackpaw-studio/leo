package cli

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestClassifyDial(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		connected bool
		want      string
	}{
		{
			name: "ehostunreach errno",
			err:  fmt.Errorf("dial tcp 10.0.0.1:80: connect: %w", syscall.EHOSTUNREACH),
			want: "denied",
		},
		{
			name: "no route to host string",
			err:  errors.New("dial tcp 10.0.0.1:80: connect: no route to host"),
			want: "denied",
		},
		{
			name: "connection refused errno",
			err:  fmt.Errorf("dial tcp 10.0.0.1:80: connect: %w", syscall.ECONNREFUSED),
			want: "granted",
		},
		{
			name: "connection refused string",
			err:  errors.New("dial tcp 10.0.0.1:80: connect: connection refused"),
			want: "granted",
		},
		{
			name:      "connected, no error",
			err:       nil,
			connected: true,
			want:      "granted",
		},
		{
			name: "timeout",
			err:  errors.New("dial tcp 10.0.0.1:80: i/o timeout"),
			want: "undetermined",
		},
		{
			name: "unrelated error",
			err:  errors.New("dial tcp: lookup foo: no such host"),
			want: "undetermined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyDial(tt.err, tt.connected)
			if got != tt.want {
				t.Errorf("classifyDial(%v, %v) = %q, want %q", tt.err, tt.connected, got, tt.want)
			}
		})
	}
}

func TestCheckServiceDriftFnReportsDrift(t *testing.T) {
	origDrift := checkServiceDriftFn
	defer func() { checkServiceDriftFn = origDrift }()

	checkServiceDriftFn = func(home string) (bool, string, error) {
		return true, "launchd job is loaded but its plist is missing", nil
	}

	drifted, detail, err := checkServiceDriftFn("/tmp/does-not-matter")
	if err != nil {
		t.Fatalf("checkServiceDriftFn() error: %v", err)
	}
	if !drifted {
		t.Fatal("expected drift to be reported")
	}
	if detail == "" {
		t.Error("expected a non-empty detail")
	}
}

func TestCheckServiceDriftFnHealthyIsSilent(t *testing.T) {
	origDrift := checkServiceDriftFn
	defer func() { checkServiceDriftFn = origDrift }()

	checkServiceDriftFn = func(home string) (bool, string, error) { return false, "", nil }

	drifted, _, err := checkServiceDriftFn("/tmp/does-not-matter")
	if err != nil {
		t.Fatalf("checkServiceDriftFn() error: %v", err)
	}
	if drifted {
		t.Error("expected no drift for the healthy case")
	}
}

func TestCheckServiceDriftFnNotLoadedIsNotAFalseAlarm(t *testing.T) {
	origDrift := checkServiceDriftFn
	defer func() { checkServiceDriftFn = origDrift }()

	checkServiceDriftFn = func(home string) (bool, string, error) { return false, "", nil }

	drifted, detail, err := checkServiceDriftFn("/tmp/does-not-matter")
	if err != nil {
		t.Fatalf("checkServiceDriftFn() error: %v", err)
	}
	if drifted {
		t.Errorf("expected not-loaded to be reported healthy, got detail: %q", detail)
	}
}

func TestNewDoctorCmd(t *testing.T) {
	cmd := newDoctorCmd()

	if cmd.Use != "doctor" {
		t.Errorf("Use = %q, want %q", cmd.Use, "doctor")
	}
	if cmd.Short == "" {
		t.Error("Short description is empty")
	}
	if cmd.Long == "" {
		t.Error("Long description is empty")
	}

	probeHostFlag := cmd.Flags().Lookup("probe-host")
	if probeHostFlag == nil {
		t.Fatal("missing --probe-host flag")
	}
	if probeHostFlag.DefValue != "" {
		t.Errorf("--probe-host default = %q, want empty", probeHostFlag.DefValue)
	}

	triggerFlag := cmd.Flags().Lookup("trigger")
	if triggerFlag == nil {
		t.Fatal("missing --trigger flag")
	}
	if triggerFlag.DefValue != "true" {
		t.Errorf("--trigger default = %q, want %q", triggerFlag.DefValue, "true")
	}
}
