package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/fatih/color"
)

// captureColorOutput redirects the prompt package's color.Output (what
// success/warn/info actually write to — see internal/prompt/prompt.go)
// to an in-memory pipe for the duration of fn, and returns everything
// written. color.Output is read fresh on every Printf/Println call, so
// reassigning it (unlike os.Stdout) reliably captures output written
// through *color.Color values created before this call.
func captureColorOutput(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := color.Output
	color.Output = w
	defer func() { color.Output = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured output: %v", err)
	}
	return string(out)
}

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

// TestReportServiceHealthDriftDetected drives reportServiceHealth (the
// function runDoctor actually calls) end to end with the drift seam
// stubbed, and asserts both on the printed output and on the exact home
// argument the seam receives — unlike calling the seam directly, this
// exercises the real wiring in doctor.go.
func TestReportServiceHealthDriftDetected(t *testing.T) {
	origDrift := checkServiceDriftFn
	origCollision := checkLegacyLabelCollisionFn
	defer func() {
		checkServiceDriftFn = origDrift
		checkLegacyLabelCollisionFn = origCollision
	}()

	const wantHome = "/tmp/does-not-matter"
	var gotHome string
	checkServiceDriftFn = func(home string) (bool, string) {
		gotHome = home
		return true, "launchd job is loaded but its plist is missing"
	}
	checkLegacyLabelCollisionFn = func(home string) (bool, string) { return false, "" }

	output := captureColorOutput(t, func() {
		reportServiceHealth(wantHome)
	})

	if gotHome != wantHome {
		t.Errorf("checkServiceDriftFn received home = %q, want %q", gotHome, wantHome)
	}
	if !doctorContainsAll(output, "drift detected", "plist is missing") {
		t.Errorf("output = %q, want it to mention the drift and its detail", output)
	}
}

// TestReportServiceHealthHealthyIsSilent verifies the "don't add noise
// on the common path" requirement: when nothing is wrong, neither the
// drift nor the legacy-collision line is printed.
func TestReportServiceHealthHealthyIsSilent(t *testing.T) {
	origDrift := checkServiceDriftFn
	origCollision := checkLegacyLabelCollisionFn
	defer func() {
		checkServiceDriftFn = origDrift
		checkLegacyLabelCollisionFn = origCollision
	}()

	checkServiceDriftFn = func(home string) (bool, string) { return false, "" }
	checkLegacyLabelCollisionFn = func(home string) (bool, string) { return false, "" }

	output := captureColorOutput(t, func() {
		reportServiceHealth("/tmp/does-not-matter")
	})

	if doctorContainsAll(output, "drift") || doctorContainsAll(output, "collision") {
		t.Errorf("expected no drift/collision noise for the healthy case, got: %q", output)
	}
}

// TestReportServiceHealthLegacyCollisionDetected exercises the fourth
// review finding's wiring: a legacy base-label collision is reported
// distinctly from drift.
func TestReportServiceHealthLegacyCollisionDetected(t *testing.T) {
	origDrift := checkServiceDriftFn
	origCollision := checkLegacyLabelCollisionFn
	defer func() {
		checkServiceDriftFn = origDrift
		checkLegacyLabelCollisionFn = origCollision
	}()

	const wantHome = "/tmp/second-checkout/.leo"
	var gotHome string
	checkServiceDriftFn = func(home string) (bool, string) { return false, "" }
	checkLegacyLabelCollisionFn = func(home string) (bool, string) {
		gotHome = home
		return true, "a legacy launchd job \"com.blackpaw.leo\" is already registered for this leo home"
	}

	output := captureColorOutput(t, func() {
		reportServiceHealth(wantHome)
	})

	if gotHome != wantHome {
		t.Errorf("checkLegacyLabelCollisionFn received home = %q, want %q", gotHome, wantHome)
	}
	if !doctorContainsAll(output, "legacy registration collision", "com.blackpaw.leo") {
		t.Errorf("output = %q, want it to mention the legacy collision and its detail", output)
	}
}

func doctorContainsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
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
