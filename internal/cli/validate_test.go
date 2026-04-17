package cli

import (
	"strings"
	"testing"
)

// TestSortFindings_SeverityOrder verifies findings are sorted ERROR, WARN,
// INFO with stable intra-bucket order.
func TestSortFindings_SeverityOrder(t *testing.T) {
	cases := []struct {
		name string
		in   []Finding
		want []string // "<severity>:<check>"
	}{
		{
			name: "mixed severities",
			in: []Finding{
				{Severity: SeverityInfo, Check: "a", Message: "m1"},
				{Severity: SeverityError, Check: "b", Message: "m2"},
				{Severity: SeverityWarn, Check: "c", Message: "m3"},
				{Severity: SeverityError, Check: "d", Message: "m4"},
				{Severity: SeverityInfo, Check: "e", Message: "m5"},
			},
			want: []string{
				"ERROR:b",
				"ERROR:d",
				"WARN:c",
				"INFO:a",
				"INFO:e",
			},
		},
		{
			name: "all info",
			in: []Finding{
				{Severity: SeverityInfo, Check: "a"},
				{Severity: SeverityInfo, Check: "b"},
			},
			want: []string{"INFO:a", "INFO:b"},
		},
		{
			name: "stable order within severity",
			in: []Finding{
				{Severity: SeverityWarn, Check: "first"},
				{Severity: SeverityWarn, Check: "second"},
				{Severity: SeverityWarn, Check: "third"},
			},
			want: []string{"WARN:first", "WARN:second", "WARN:third"},
		},
		{
			name: "empty",
			in:   nil,
			want: []string{},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sorted := sortFindings(tc.in)
			got := make([]string, 0, len(sorted))
			for _, f := range sorted {
				got = append(got, string(f.Severity)+":"+f.Check)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestCountSeverity verifies severity tally.
func TestCountSeverity(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityError},
		{Severity: SeverityError},
		{Severity: SeverityWarn},
		{Severity: SeverityInfo},
		{Severity: SeverityInfo},
		{Severity: SeverityInfo},
	}
	cases := []struct {
		sev  Severity
		want int
	}{
		{SeverityError, 2},
		{SeverityWarn, 1},
		{SeverityInfo, 3},
	}
	for _, tc := range cases {
		if got := countSeverity(findings, tc.sev); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.sev, got, tc.want)
		}
	}
}

// TestPluralize covers singular vs plural pluralization.
func TestPluralize(t *testing.T) {
	cases := []struct {
		n    int
		noun string
		want string
	}{
		{0, "error", "0 errors"},
		{1, "error", "1 error"},
		{2, "error", "2 errors"},
		{1, "warning", "1 warning"},
		{3, "warning", "3 warnings"},
	}
	for _, tc := range cases {
		if got := pluralize(tc.n, tc.noun); got != tc.want {
			t.Errorf("pluralize(%d, %q) = %q; want %q", tc.n, tc.noun, got, tc.want)
		}
	}
}

// TestSeverityRank covers rank ordering and unknown values.
func TestSeverityRank(t *testing.T) {
	cases := []struct {
		in   Severity
		want int
	}{
		{SeverityError, 0},
		{SeverityWarn, 1},
		{SeverityInfo, 2},
		{Severity("bogus"), 3},
	}
	for _, tc := range cases {
		if got := severityRank(tc.in); got != tc.want {
			t.Errorf("severityRank(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

// TestEmitValidateJSON_ExitCode verifies JSON emission returns an error when
// any finding is an ERROR, and nil otherwise.
func TestEmitValidateJSON_ExitCode(t *testing.T) {
	cases := []struct {
		name       string
		findings   []Finding
		wantErrSub string // substring expected in error, or "" for nil
	}{
		{
			name:       "all info",
			findings:   []Finding{{Severity: SeverityInfo, Check: "a"}},
			wantErrSub: "",
		},
		{
			name: "with warning",
			findings: []Finding{
				{Severity: SeverityWarn, Check: "a"},
			},
			wantErrSub: "",
		},
		{
			name: "with error",
			findings: []Finding{
				{Severity: SeverityError, Check: "a"},
			},
			wantErrSub: "error",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_ = captureStdoutForConfigTests(t, func() {
				err := emitValidateJSON(tc.findings)
				if tc.wantErrSub == "" && err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				if tc.wantErrSub != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErrSub)) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrSub, err)
				}
			})
		})
	}
}

// TestEmitValidateText_ExitCode verifies text emission returns an error when
// any finding is an ERROR, and nil otherwise.
func TestEmitValidateText_ExitCode(t *testing.T) {
	cases := []struct {
		name    string
		find    []Finding
		wantErr bool
	}{
		{"all info", []Finding{{Severity: SeverityInfo, Check: "ok"}}, false},
		{"warn only", []Finding{{Severity: SeverityWarn, Check: "tmux"}}, false},
		{"error", []Finding{{Severity: SeverityError, Check: "claude"}}, true},
		{"error and warn", []Finding{
			{Severity: SeverityError, Check: "claude"},
			{Severity: SeverityWarn, Check: "tmux"},
		}, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_ = captureStdoutForConfigTests(t, func() {
				err := emitValidateText(tc.find, nil)
				if tc.wantErr && err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !tc.wantErr && err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			})
		})
	}
}
