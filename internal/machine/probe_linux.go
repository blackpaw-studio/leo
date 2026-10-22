//go:build linux

package machine

import (
	"fmt"
	"io"
	"os"
	"runtime"
)

type linuxProbe struct {
	stateDir string
	cores    int
}

// NewProbe returns the Linux probe: /proc for CPU and memory, statfs on
// stateDir for disk.
func NewProbe(stateDir string) Probe {
	p := &linuxProbe{stateDir: stateDir, cores: runtime.NumCPU()}
	// All CPUs the kernel knows, not runtime.NumCPU, which follows affinity.
	if _, n, err := readProcStat(); err == nil && n > 0 {
		p.cores = n
	}
	return p
}

func (p *linuxProbe) Cores() int { return p.cores }

func (p *linuxProbe) CPUTicks() (CPUTicks, error) {
	ticks, _, err := readProcStat()
	return ticks, err
}

func (p *linuxProbe) Memory() (Memory, error) {
	return readProc("/proc/meminfo", parseMeminfo)
}

func (p *linuxProbe) Disk() (Disk, error) { return statfsDisk(p.stateDir) }

func readProcStat() (CPUTicks, int, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return CPUTicks{}, 0, err
	}
	defer f.Close()
	return parseProcStat(f)
}

func readProc[T any](path string, parse func(io.Reader) (T, error)) (T, error) {
	f, err := os.Open(path)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	return parse(f)
}
