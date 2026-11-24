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

func TestPermissionInput(t *testing.T) {
	for _, tc := range []struct {
		name, tool, input, want string
	}{
		{"bash is the full command", "Bash", `{"command":"  rm -rf /tmp/x && echo hi  ","description":"d"}`, "rm -rf /tmp/x && echo hi"},
		{"bash keeps a multi-line command", "Bash", `{"command":"a\nb"}`, "a\nb"},
		{"read abbreviates home", "Read", `{"file_path":"/home/ev/src/a.go","offset":3}`, "~/src/a.go"},
		{"edit is the path, never the strings", "Edit", `{"file_path":"/home/ev/a.go","old_string":"SECRET","new_string":"NEW"}`, "~/a.go"},
		{"write is the path, never the content", "Write", `{"file_path":"/etc/x","content":"SECRET"}`, "/etc/x"},
		{"notebook edit is the path", "NotebookEdit", `{"notebook_path":"/home/ev/n.ipynb","new_source":"SECRET"}`, "~/n.ipynb"},
		{"grep adds where it searches", "Grep", `{"pattern":"foo.*bar","path":"/home/ev/src"}`, "foo.*bar in ~/src"},
		{"grep without a path is the pattern", "Grep", `{"pattern":"foo"}`, "foo"},
		{"glob adds where it searches", "Glob", `{"pattern":"**/*.go","path":"/srv"}`, "**/*.go in /srv"},
		{"webfetch drops credentials, query and fragment", "WebFetch", `{"url":"https://user:pw@example.com:8443/a/b?token=1#frag","prompt":"p"}`, "https://example.com:8443/a/b"},
		{"unparseable url is empty", "WebFetch", `{"url":"::"}`, ""},
		{"unknown tool is empty", "mcp__x__y", `{"secret":"s"}`, ""},
		{"non-object input is empty", "Bash", `"echo"`, ""},
		{"missing command is empty", "Bash", `{}`, ""},
		{"blank command is empty", "Bash", `{"command":"   "}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PermissionInput(tc.tool, json.RawMessage(tc.input), "/home/ev"); got != tc.want {
				t.Fatalf("PermissionInput = %q, want %q", got, tc.want)
			}
		})
	}
}
