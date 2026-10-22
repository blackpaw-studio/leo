//go:build darwin

package machine

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// Mach host_statistics flavors and their sizes in 32-bit words
// (osfmk/mach/host_info.h, vm_statistics.h).
const (
	hostCPULoadInfo  = 3
	hostVMInfo64     = 4
	cpuLoadInfoCount = 4 // natural_t cpu_ticks[CPU_STATE_MAX]
	vmInfo64Count    = 38
	kernSuccess      = 0
)

// cpu_ticks indices.
const (
	cpuStateUser = iota
	cpuStateSystem
	cpuStateIdle
	cpuStateNice
)

// vmStatistics64 is xnu's vm_statistics64 (rev1, 152 bytes). Field order and
// widths must match the kernel's; the layout test pins the offsets we read.
type vmStatistics64 struct {
	FreeCount                          uint32
	ActiveCount                        uint32
	InactiveCount                      uint32
	WireCount                          uint32
	ZeroFillCount                      uint64
	Reactivations                      uint64
	Pageins                            uint64
	Pageouts                           uint64
	Faults                             uint64
	CowFaults                          uint64
	Lookups                            uint64
	Hits                               uint64
	Purges                             uint64
	PurgeableCount                     uint32
	SpeculativeCount                   uint32
	Decompressions                     uint64
	Compressions                       uint64
	Swapins                            uint64
	Swapouts                           uint64
	CompressorPageCount                uint32
	ThrottledCount                     uint32
	ExternalPageCount                  uint32
	InternalPageCount                  uint32
	TotalUncompressedPagesInCompressor uint64
}

type darwinProbe struct {
	stateDir string
	cores    int
	// setupErr is why the Mach bindings are unusable; CPU and memory reads
	// return it.
	setupErr   error
	totalBytes uint64

	host            uint32 // cached mach_host_self(); each call adds a send right
	pageSize        uint64
	hostStatistics  func(host uint32, flavor int32, info unsafe.Pointer, count *uint32) int32
	hostStatistics6 func(host uint32, flavor int32, info unsafe.Pointer, count *uint32) int32
}

// NewProbe returns the macOS probe. It talks to libSystem through purego
// (no cgo, so CGO_ENABLED=0 release builds work) and never spawns a
// subprocess.
func NewProbe(stateDir string) Probe {
	p := &darwinProbe{stateDir: stateDir, cores: runtime.NumCPU()}
	if n, err := unix.SysctlUint32("hw.logicalcpu"); err == nil && n > 0 {
		p.cores = int(n)
	}
	p.setupErr = p.bind()
	return p
}

// bind resolves the libSystem symbols with Dlsym before RegisterFunc, so a
// missing symbol is an error rather than a panic.
func (p *darwinProbe) bind() error {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	p.totalBytes = total

	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fmt.Errorf("dlopen libSystem: %w", err)
	}
	sym := func(name string) (uintptr, error) {
		addr, err := purego.Dlsym(lib, name)
		if err != nil {
			return 0, fmt.Errorf("dlsym %s: %w", name, err)
		}
		return addr, nil
	}
	hostSelf, err := sym("mach_host_self")
	if err != nil {
		return err
	}
	stats, err := sym("host_statistics")
	if err != nil {
		return err
	}
	stats64, err := sym("host_statistics64")
	if err != nil {
		return err
	}
	pageSizeAddr, err := sym("vm_kernel_page_size")
	if err != nil {
		return err
	}

	var machHostSelf func() uint32
	purego.RegisterFunc(&machHostSelf, hostSelf)
	purego.RegisterFunc(&p.hostStatistics, stats)
	purego.RegisterFunc(&p.hostStatistics6, stats64)
	p.host = machHostSelf()
	// vm_kernel_page_size is the unit of the page counts, and stays correct
	// for an x86_64 process under Rosetta where hw.pagesize says 4096.
	// Double indirection keeps go vet's unsafeptr check quiet.
	p.pageSize = **(**uint64)(unsafe.Pointer(&pageSizeAddr))
	if p.pageSize == 0 {
		return fmt.Errorf("vm_kernel_page_size is 0")
	}
	return nil
}

func (p *darwinProbe) Cores() int { return p.cores }

func (p *darwinProbe) CPUTicks() (CPUTicks, error) {
	if p.setupErr != nil {
		return CPUTicks{}, p.setupErr
	}
	var ticks [cpuLoadInfoCount]uint32
	count := uint32(cpuLoadInfoCount)
	if kr := p.hostStatistics(p.host, hostCPULoadInfo, unsafe.Pointer(&ticks[0]), &count); kr != kernSuccess {
		return CPUTicks{}, fmt.Errorf("host_statistics(HOST_CPU_LOAD_INFO): kern_return_t %d", kr)
	}
	return CPUTicks{
		Busy: uint64(ticks[cpuStateUser]) + uint64(ticks[cpuStateSystem]) + uint64(ticks[cpuStateNice]),
		Idle: uint64(ticks[cpuStateIdle]),
	}, nil
}

func (p *darwinProbe) Memory() (Memory, error) {
	if p.setupErr != nil {
		return Memory{}, p.setupErr
	}
	vm := new(vmStatistics64)
	count := uint32(vmInfo64Count)
	if kr := p.hostStatistics6(p.host, hostVMInfo64, unsafe.Pointer(vm), &count); kr != kernSuccess {
		return Memory{}, fmt.Errorf("host_statistics64(HOST_VM_INFO64): kern_return_t %d", kr)
	}
	used := min(darwinUsed(vm.InternalPageCount, vm.PurgeableCount, vm.WireCount, vm.CompressorPageCount, p.pageSize), p.totalBytes)
	return Memory{UsedBytes: used, TotalBytes: p.totalBytes}, nil
}

func (p *darwinProbe) Disk() (Disk, error) { return statfsDisk(p.stateDir) }
