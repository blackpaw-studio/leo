//go:build !darwin && !linux

package machine

import (
	"errors"
	"testing"
)

func TestOtherPlatformProbeReportsOnlyCores(t *testing.T) {
	p := NewProbe(t.TempDir())

	if p.Cores() < 1 {
		t.Fatalf("cores = %d, want >= 1", p.Cores())
	}
	if _, err := p.CPUTicks(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("CPUTicks err = %v, want ErrUnsupported", err)
	}
	if _, err := p.Memory(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Memory err = %v, want ErrUnsupported", err)
	}
	if _, err := p.Disk(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Disk err = %v, want ErrUnsupported", err)
	}
}
