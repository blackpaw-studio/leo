// Package machine samples whole-machine CPU, memory and disk usage for the
// machine_stats routes. Probes read syscalls or procfs only (no
// subprocesses) and sit behind Probe so the Sampler's cache, baseline and
// null handling can be tested with fakes.
package machine

import "time"

// Stats is one machine sample. A section whose probe failed is nil and
// serializes as JSON null; the error is logged, never reported.
type Stats struct {
	SampledAt time.Time `json:"sampled_at"`
	CPU       CPU       `json:"cpu"`
	Memory    *Memory   `json:"memory"`
	Disk      *Disk     `json:"disk"`
}

// CPU is whole-machine usage since the previous sample. Percent is nil when
// there is no usable baseline.
type CPU struct {
	Percent *float64 `json:"percent"`
	Cores   int      `json:"cores"`
}

// Memory is used and total physical memory in bytes.
type Memory struct {
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

// Disk is the filesystem holding Path, in bytes.
type Disk struct {
	Path       string `json:"path"`
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

// CPUTicks are cumulative scheduler ticks. Busy and Idle only mean something
// as deltas between two readings.
type CPUTicks struct {
	Busy uint64
	Idle uint64
}

// Probe reads raw machine counters. Implementations are per-platform; see
// NewProbe.
type Probe interface {
	// Cores is the logical CPU count. It never fails.
	Cores() int
	CPUTicks() (CPUTicks, error)
	Memory() (Memory, error)
	Disk() (Disk, error)
}
