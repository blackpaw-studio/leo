package agent

// ResumeArgs rewrites stored claude args so a restored or resumed agent rejoins
// a prior session. Any existing `--session-id`/`--resume` pair is stripped
// (defensive: never pass two session-selection flags) before appending
// `--resume <sessionID>`. An empty sessionID returns the args with those flags
// stripped — the caller has chosen a fresh spawn.
func ResumeArgs(args []string, sessionID string) []string {
	cleaned := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		if args[i] == "--session-id" || args[i] == "--resume" {
			if i+1 < len(args) {
				i++ // skip the value too
			}
			continue // drop the flag even if it has no value (never pass a naked flag)
		}
		cleaned = append(cleaned, args[i])
	}
	if sessionID == "" {
		return cleaned
	}
	return append(cleaned, "--resume", sessionID)
}
