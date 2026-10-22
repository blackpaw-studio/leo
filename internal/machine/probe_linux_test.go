//go:build linux

package machine

import "testing"

func TestLinuxProbeLiveRead(t *testing.T) {
	p := NewProbe(t.TempDir())

	if p.Cores() < 1 {
		t.Fatalf("cores = %d, want >= 1", p.Cores())
	}
	first, err := p.CPUTicks()
	if err != nil {
		t.Fatalf("CPUTicks: %v", err)
	}
	second, err := p.CPUTicks()
	if err != nil {
		t.Fatalf("CPUTicks: %v", err)
	}
	if second.Busy < first.Busy || second.Idle < first.Idle {
		t.Fatalf("ticks went down: %+v -> %+v", first, second)
	}
	mem, err := p.Memory()
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	if mem.TotalBytes == 0 || mem.UsedBytes > mem.TotalBytes {
		t.Fatalf("memory = %+v, want 0 < used <= total", mem)
	}
	disk, err := p.Disk()
	if err != nil {
		t.Fatalf("Disk: %v", err)
	}
	if disk.TotalBytes == 0 || disk.UsedBytes > disk.TotalBytes {
		t.Fatalf("disk = %+v, want 0 < used <= total", disk)
	}
}
