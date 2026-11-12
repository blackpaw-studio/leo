package observe

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// shortstatRe matches git's one summary line:
// " 3 files changed, 10 insertions(+), 2 deletions(-)" where the insertions
// and deletions clauses are each omitted when zero.
var shortstatRe = regexp.MustCompile(`^(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?$`)

// ParseShortstat reads the output of `git diff --shortstat`: empty output is
// a clean tree. Anything else that is not one summary line is an error.
func ParseShortstat(out string) (GitStats, error) {
	line := strings.TrimSpace(out)
	if line == "" {
		return GitStats{}, nil
	}
	m := shortstatRe.FindStringSubmatch(line)
	if m == nil {
		return GitStats{}, fmt.Errorf("unrecognised shortstat output %q", truncateRunes(line, 80))
	}
	var stats GitStats
	for i, dst := range []*int64{&stats.Added, &stats.Removed} {
		digits := m[i+2] // groups 2 and 3; group 1 is the file count
		if digits == "" {
			continue
		}
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return GitStats{}, fmt.Errorf("shortstat count %q: %w", digits, err)
		}
		*dst = n
	}
	return stats, nil
}
