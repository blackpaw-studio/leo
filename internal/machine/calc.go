package machine

// diskFromStatfs derives Disk from statfs counts: total is blocks*bsize and
// used is total less the blocks available to unprivileged users, matching
// df. Used never underflows.
func diskFromStatfs(path string, blocks, bavail, bsize uint64) Disk {
	total := blocks * bsize
	avail := min(bavail*bsize, total)
	return Disk{Path: path, UsedBytes: total - avail, TotalBytes: total}
}

// darwinUsed is Activity Monitor's "Memory Used": app (internal pages less
// purgeable) + wired + compressor, in bytes. Page counts are the 32-bit
// fields of vm_statistics64.
func darwinUsed(internal, purgeable, wire, compressor uint32, pageSize uint64) uint64 {
	app := uint64(0)
	if internal > purgeable {
		app = uint64(internal - purgeable)
	}
	return (app + uint64(wire) + uint64(compressor)) * pageSize
}
