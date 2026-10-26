package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolActivity(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct {
		name, tool, input     string
		wantTool, wantSummary string
	}{
		{"bash keeps only the first word", "Bash", `{"command":"  rm -rf /tmp/x && echo hi"}`, "Bash", "rm"},
		{"read abbreviates home", "Read", `{"file_path":"/home/ev/src/a.go"}`, "Read", "~/src/a.go"},
		{"edit outside home is verbatim", "Edit", `{"file_path":"/etc/hosts"}`, "Edit", "/etc/hosts"},
		{"home itself", "Write", `{"file_path":"/home/ev"}`, "Write", "~"},
		{"sibling of home is not abbreviated", "Read", `{"file_path":"/home/evil/a"}`, "Read", "/home/evil/a"},
		{"grep shows the pattern", "Grep", `{"pattern":"foo.*bar","path":"/secret"}`, "Grep", "foo.*bar"},
		{"glob shows the pattern", "Glob", `{"pattern":"**/*.go"}`, "Glob", "**/*.go"},
		{"webfetch shows the host only", "WebFetch", `{"url":"https://user:pw@example.com:8443/a?token=1"}`, "WebFetch", "example.com"},
		{"unparseable url has no summary", "WebFetch", `{"url":"::"}`, "WebFetch", ""},
		{"unknown tool has no summary", "mcp__x__y", `{"secret":"s"}`, "mcp__x__y", ""},
		{"non-object input", "Bash", `"echo"`, "Bash", ""},
		{"missing command", "Bash", `{}`, "Bash", ""},
		{"summary is capped", "Grep", `{"pattern":"` + long + `"}`, "Grep", long[:MaxToolFieldChars]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotTool, gotSummary := ToolActivity(tc.tool, json.RawMessage(tc.input), "/home/ev")

			if gotTool != tc.wantTool || gotSummary != tc.wantSummary {
				t.Fatalf("ToolActivity = (%q, %q), want (%q, %q)", gotTool, gotSummary, tc.wantTool, tc.wantSummary)
			}
		})
	}
}
