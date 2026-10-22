package machine

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// procfs parsers carry no build tag so they are testable on every platform;
// only probe_linux.go calls them in production.

// parseProcStat reads the aggregate "cpu" line and counts "cpuN" lines.
// Busy is user+nice+system+irq+softirq+steal; Idle is idle+iowait. guest and
// guest_nice are already inside user/nice, so they are not added again.
//
// The cpu lines come first in /proc/stat, and reading stops at the first line
// that doesn't start with "cpu": the intr line after them can run past any
// fixed token limit on a large host.
func parseProcStat(r io.Reader) (CPUTicks, int, error) {
	var (
		ticks CPUTicks
		cores int
		found bool
	)
	br := bufio.NewReader(r)
	for {
		head, err := br.Peek(len("cpu"))
		if err == io.EOF || (err == nil && string(head) != "cpu") {
			break
		}
		if err != nil {
			return CPUTicks{}, 0, err
		}
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return CPUTicks{}, 0, err
		}
		fields := strings.Fields(line)
		switch {
		case fields[0] == "cpu":
			t, terr := ticksFromFields(fields[1:])
			if terr != nil {
				return CPUTicks{}, 0, terr
			}
			ticks, found = t, true
		case isCoreLabel(fields[0]):
			cores++
		}
	}
	if !found {
		return CPUTicks{}, 0, errors.New("no aggregate cpu line in /proc/stat")
	}
	return ticks, cores, nil
}

// isCoreLabel matches "cpu0", "cpu15", but not "cpu" or "cpufreq".
func isCoreLabel(s string) bool {
	n, ok := strings.CutPrefix(s, "cpu")
	if !ok || n == "" {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// ticksFromFields maps /proc/stat's user nice system idle [iowait irq
// softirq steal ...] fields; kernels before 2.6 report only the first four.
func ticksFromFields(fields []string) (CPUTicks, error) {
	if len(fields) < 4 {
		return CPUTicks{}, fmt.Errorf("cpu line has %d fields, want at least 4", len(fields))
	}
	const maxUsed = 8
	vals := make([]uint64, maxUsed)
	for i := 0; i < min(len(fields), maxUsed); i++ {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return CPUTicks{}, fmt.Errorf("cpu field %d: %w", i, err)
		}
		vals[i] = v
	}
	user, nice, system, idle, iowait, irq, softirq, steal := vals[0], vals[1], vals[2], vals[3], vals[4], vals[5], vals[6], vals[7]
	return CPUTicks{
		Busy: user + nice + system + irq + softirq + steal,
		Idle: idle + iowait,
	}, nil
}

// parseMeminfo derives Memory from /proc/meminfo: used = MemTotal -
// MemAvailable, or MemTotal - (MemFree + Buffers + Cached) on kernels
// without MemAvailable. Used is clamped to [0, total].
func parseMeminfo(r io.Reader) (Memory, error) {
	kb := map[string]uint64{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		kb[key] = v
	}
	if err := sc.Err(); err != nil {
		return Memory{}, err
	}
	total, ok := kb["MemTotal"]
	if !ok {
		return Memory{}, errors.New("no MemTotal in /proc/meminfo")
	}
	avail, ok := kb["MemAvailable"]
	if !ok {
		avail = kb["MemFree"] + kb["Buffers"] + kb["Cached"]
	}
	const bytesPerKB = 1024
	return Memory{
		UsedBytes:  (total - min(avail, total)) * bytesPerKB,
		TotalBytes: total * bytesPerKB,
	}, nil
}
