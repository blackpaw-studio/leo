package machine

import (
	"strings"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantTicks CPUTicks
		wantCores int
		wantErr   bool
	}{
		{
			name: "full line excludes guest and counts iowait as idle",
			in: "cpu  10 20 30 400 50 6 7 8 900 1000\n" +
				"cpu0 5 10 15 200 25 3 3 4 450 500\n" +
				"cpu1 5 10 15 200 25 3 4 4 450 500\n" +
				"intr 1\n",
			// Busy sums user, nice, system, irq, softirq and steal; idle adds iowait.
			wantTicks: CPUTicks{Busy: 10 + 20 + 30 + 6 + 7 + 8, Idle: 400 + 50},
			wantCores: 2,
		},
		{
			name:      "old four-field line",
			in:        "cpu  1 2 3 4\ncpu0 1 2 3 4\n",
			wantTicks: CPUTicks{Busy: 6, Idle: 4},
			wantCores: 1,
		},
		{
			name:    "missing aggregate cpu line",
			in:      "cpu0 1 2 3 4\nintr 1\n",
			wantErr: true,
		},
		{
			name:    "aggregate line too short",
			in:      "cpu  1 2 3\n",
			wantErr: true,
		},
		{
			name:    "non-numeric field",
			in:      "cpu  1 2 x 4\n",
			wantErr: true,
		},
		{
			name:      "cpu-prefixed non-core lines are not cores",
			in:        "cpu  1 1 1 1\ncpufreq 9\ncpu0 1 1 1 1\n",
			wantTicks: CPUTicks{Busy: 3, Idle: 1},
			wantCores: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ticks, cores, err := parseProcStat(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if ticks != tt.wantTicks || cores != tt.wantCores {
				t.Fatalf("got %+v/%d, want %+v/%d", ticks, cores, tt.wantTicks, tt.wantCores)
			}
		})
	}
}

func TestParseProcStatSurvivesHugeLinesAfterCPULines(t *testing.T) {
	// Large hosts have an intr line well past bufio.Scanner's 64 KiB token limit.
	in := "cpu  1 2 3 4 5 6 7 8 9 10\ncpu0 1 2 3 4 5 6 7 8 9 10\ncpu1 1 2 3 4 5 6 7 8 9 10\n" +
		"intr " + strings.Repeat("0 ", 100*1024) + "\nctxt 5\n"

	ticks, cores, err := parseProcStat(strings.NewReader(in))

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := (CPUTicks{Busy: 1 + 2 + 3 + 6 + 7 + 8, Idle: 4 + 5}); ticks != want || cores != 2 {
		t.Fatalf("got %+v/%d, want %+v/2", ticks, cores, want)
	}
}

func TestParseMeminfo(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Memory
		wantErr bool
	}{
		{
			name: "uses MemAvailable and converts kB to bytes",
			in:   "MemTotal:       16000 kB\nMemFree: 1 kB\nMemAvailable:    4000 kB\n",
			want: Memory{UsedBytes: 12000 * 1024, TotalBytes: 16000 * 1024},
		},
		{
			name: "falls back to free+buffers+cached",
			in:   "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 150 kB\n",
			want: Memory{UsedBytes: 700 * 1024, TotalBytes: 1000 * 1024},
		},
		{
			name:    "missing MemTotal",
			in:      "MemFree: 1 kB\nMemAvailable: 1 kB\n",
			wantErr: true,
		},
		{
			name: "available above total clamps used to zero",
			in:   "MemTotal: 100 kB\nMemAvailable: 200 kB\n",
			want: Memory{UsedBytes: 0, TotalBytes: 100 * 1024},
		},
		{
			name: "malformed lines are ignored",
			in:   "garbage\nMemTotal: 100 kB\nBuffers: nope kB\n:\nMemAvailable: 40 kB\n",
			want: Memory{UsedBytes: 60 * 1024, TotalBytes: 100 * 1024},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMeminfo(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
