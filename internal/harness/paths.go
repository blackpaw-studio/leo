package harness

import "path/filepath"

// SamePath reports whether a and b refer to the same filesystem location.
// A plain Clean comparison is insufficient: a harness reports its own
// os.Getwd()-derived path, and on macOS /tmp symlinks to /private/tmp — a
// leo workspace configured as /tmp/... would otherwise never match the
// harness's self-reported /private/tmp/... Falls back to the Clean
// comparison when EvalSymlinks fails on either side (e.g. the dir no longer
// exists) rather than erroring, since a missed match is tolerable to
// callers (they keep polling).
func SamePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, aErr := filepath.EvalSymlinks(a)
	rb, bErr := filepath.EvalSymlinks(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return ra == rb
}
