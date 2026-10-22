//go:build !darwin && !linux

package machine

import (
	"errors"
	"runtime"
)

type unsupportedProbe struct{}

// NewProbe returns the fallback probe for platforms without a native
// implementation: only the core count is known, so every section but
// cpu.cores serializes as null.
func NewProbe(string) Probe { return unsupportedProbe{} }

func (unsupportedProbe) Cores() int { return runtime.NumCPU() }

func (unsupportedProbe) CPUTicks() (CPUTicks, error) { return CPUTicks{}, errors.ErrUnsupported }

func (unsupportedProbe) Memory() (Memory, error) { return Memory{}, errors.ErrUnsupported }

func (unsupportedProbe) Disk() (Disk, error) { return Disk{}, errors.ErrUnsupported }
