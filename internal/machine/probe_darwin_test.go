//go:build darwin

package machine

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The layout is xnu's vm_statistics64 (osfmk/mach/vm_statistics.h, rev1).
func TestVMStatistics64MatchesXNULayout(t *testing.T) {
	var vm vmStatistics64

	if got := unsafe.Sizeof(vm); got != 152 {
		t.Fatalf("sizeof = %d, want 152", got)
	}
	if got := unsafe.Offsetof(vm.InternalPageCount); got != 140 {
		t.Fatalf("offsetof internal_page_count = %d, want 140", got)
	}
	if got := unsafe.Offsetof(vm.PurgeableCount); got != 88 {
		t.Fatalf("offsetof purgeable_count = %d, want 88", got)
	}
	if got := unsafe.Offsetof(vm.CompressorPageCount); got != 128 {
		t.Fatalf("offsetof compressor_page_count = %d, want 128", got)
	}
	if vmInfo64Count != 38 {
		t.Fatalf("vmInfo64Count = %d, want 38", vmInfo64Count)
	}
}

func TestDarwinProbeLiveRead(t *testing.T) {
	p := NewProbe(t.TempDir())

	wantCores, err := unix.SysctlUint32("hw.logicalcpu")
	if err != nil {
		t.Fatal(err)
	}
	if p.Cores() != int(wantCores) {
		t.Fatalf("cores = %d, want %d", p.Cores(), wantCores)
	}

	first, err := p.CPUTicks()
	if err != nil {
		t.Fatalf("CPUTicks: %v", err)
	}
	second, err := p.CPUTicks()
	if err != nil {
		t.Fatalf("CPUTicks: %v", err)
	}
	if first.Busy+first.Idle == 0 {
		t.Fatalf("ticks are zero: %+v", first)
	}
	if second.Busy < first.Busy || second.Idle < first.Idle {
		t.Fatalf("ticks went down: %+v -> %+v", first, second)
	}

	wantTotal, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := p.Memory()
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	if mem.TotalBytes != wantTotal {
		t.Fatalf("total = %d, want hw.memsize %d", mem.TotalBytes, wantTotal)
	}
	if mem.UsedBytes == 0 || mem.UsedBytes > mem.TotalBytes {
		t.Fatalf("used = %d of %d, want 0 < used <= total", mem.UsedBytes, mem.TotalBytes)
	}

	disk, err := p.Disk()
	if err != nil {
		t.Fatalf("Disk: %v", err)
	}
	if disk.TotalBytes == 0 || disk.UsedBytes > disk.TotalBytes {
		t.Fatalf("disk = %+v, want 0 < used <= total", disk)
	}
}
