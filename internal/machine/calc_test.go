package machine

import "testing"

func TestDiskFromStatfs(t *testing.T) {
	tests := []struct {
		name                 string
		blocks, bavail, bsiz uint64
		want                 Disk
	}{
		{"normal", 1000, 400, 4096, Disk{Path: "/s", UsedBytes: 600 * 4096, TotalBytes: 1000 * 4096}},
		{"bavail above blocks clamps used to zero", 100, 200, 512, Disk{Path: "/s", UsedBytes: 0, TotalBytes: 100 * 512}},
		{"empty filesystem", 0, 0, 4096, Disk{Path: "/s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diskFromStatfs("/s", tt.blocks, tt.bavail, tt.bsiz); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDarwinUsed(t *testing.T) {
	tests := []struct {
		name                                  string
		internal, purgeable, wire, compressed uint32
		pageSize                              uint64
		want                                  uint64
	}{
		{"app plus wired plus compressed", 1000, 200, 300, 100, 16384, (800 + 300 + 100) * 16384},
		{"purgeable above internal clamps app to zero", 100, 500, 300, 100, 4096, (300 + 100) * 4096},
		{"no overflow at 32-bit page counts", 4_000_000_000, 0, 4_000_000_000, 0, 16384, 8_000_000_000 * 16384},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := darwinUsed(tt.internal, tt.purgeable, tt.wire, tt.compressed, tt.pageSize)
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}
