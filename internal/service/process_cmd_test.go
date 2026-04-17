package service

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildClaudeShellCmd(t *testing.T) {
	baseArgs := []string{"--model", "sonnet"}

	tests := []struct {
		name        string
		args        []string
		spec        ProcessSpec
		pathEnv     string
		wantContain []string
		wantMissing []string
		wantWarns   []string
	}{
		{
			name: "clean env map passes through",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
				Env:     map[string]string{"FOO": "bar"},
			},
			pathEnv: "/usr/bin:/bin",
			wantContain: []string{
				"export FOO='bar';",
				"export LEO_PROCESS_NAME='alpha';",
				"export LEO_TMUX_PATH=",
				"export LEO_WEB_PORT=8370;",
				"export PATH='/usr/bin:/bin';",
				"--model",
				"sonnet",
			},
		},
		{
			name: "malicious env key is dropped",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
				Env: map[string]string{
					"GOOD":       "ok",
					"X;rm -rf /": "y",
				},
			},
			pathEnv: "/usr/bin",
			wantContain: []string{
				"export GOOD='ok';",
			},
			wantMissing: []string{
				"X;rm -rf /",
				"export X;rm",
				"=y",
			},
			wantWarns: []string{
				`dropping invalid env key "X;rm -rf /"`,
			},
		},
		{
			name: "malicious env value is quoted literally",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
				Env:     map[string]string{"X": "$(whoami)"},
			},
			pathEnv: "/usr/bin",
			wantContain: []string{
				"export X='$(whoami)';",
			},
			wantMissing: []string{
				// bare $(whoami) outside single quotes would be shell-expanded
				" $(whoami) ",
				"=$(whoami);",
			},
		},
		{
			name: "value with single quote is escaped",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
				Env:     map[string]string{"X": "it's evil"},
			},
			pathEnv: "/usr/bin",
			wantContain: []string{
				`export X='it'\''s evil';`,
			},
		},
		{
			name: "invalid WebPort is rejected and export omitted",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "80; rm -rf",
			},
			pathEnv: "/usr/bin",
			wantMissing: []string{
				"LEO_WEB_PORT",
				"80; rm -rf",
				"rm -rf",
			},
			wantWarns: []string{
				`dropping invalid LEO_WEB_PORT "80; rm -rf"`,
			},
		},
		{
			name: "empty WebPort simply omits the export with no warning",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "",
			},
			pathEnv: "/usr/bin",
			wantMissing: []string{
				"LEO_WEB_PORT",
			},
		},
		{
			name: "empty PATH env omits PATH export",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
			},
			pathEnv: "",
			wantMissing: []string{
				"export PATH=",
			},
			wantContain: []string{
				"export LEO_PROCESS_NAME='alpha';",
				"export LEO_WEB_PORT=8370;",
			},
		},
		{
			name: "claude path and args are shell-quoted",
			args: []string{"--append-system-prompt", "hello $USER"},
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
			},
			pathEnv: "/usr/bin",
			wantContain: []string{
				"'--append-system-prompt'",
				"'hello $USER'",
			},
			wantMissing: []string{
				// unquoted $USER would be expanded
				" hello $USER ",
			},
		},
		{
			name: "keys with leading digit are rejected",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
				Env:     map[string]string{"1BAD": "oops", "OK": "fine"},
			},
			pathEnv:     "/usr/bin",
			wantContain: []string{"export OK='fine';"},
			wantMissing: []string{"export 1BAD"},
			wantWarns: []string{
				`dropping invalid env key "1BAD"`,
			},
		},
		{
			name: "empty key string is rejected",
			args: baseArgs,
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
				Env:     map[string]string{"": "oops", "OK": "fine"},
			},
			pathEnv:     "/usr/bin",
			wantContain: []string{"export OK='fine';"},
			wantMissing: []string{"export ='oops'"},
			wantWarns: []string{
				`dropping invalid env key ""`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var warn bytes.Buffer
			got := buildClaudeShellCmd("/usr/local/bin/claude", tt.args, "/usr/local/bin/tmux", tt.spec, tt.pathEnv, &warn)

			for _, sub := range tt.wantContain {
				if !strings.Contains(got, sub) {
					t.Errorf("cmd missing %q\nfull cmd: %s", sub, got)
				}
			}
			for _, sub := range tt.wantMissing {
				if strings.Contains(got, sub) {
					t.Errorf("cmd should not contain %q\nfull cmd: %s", sub, got)
				}
			}
			warnStr := warn.String()
			for _, w := range tt.wantWarns {
				if !strings.Contains(warnStr, w) {
					t.Errorf("expected warning %q, got warnings: %q", w, warnStr)
				}
			}
			if len(tt.wantWarns) == 0 && warnStr != "" {
				t.Errorf("expected no warnings, got: %q", warnStr)
			}
		})
	}
}

func TestBuildClaudeShellCmd_DeterministicOrder(t *testing.T) {
	// Maps have non-deterministic iteration order in Go. Make sure the
	// helper produces a stable string across calls so logs and tests
	// stay readable.
	spec := ProcessSpec{
		Name:    "p",
		WebPort: "8370",
		Env: map[string]string{
			"ZETA":  "z",
			"ALPHA": "a",
			"MIKE":  "m",
		},
	}
	first := buildClaudeShellCmd("/c", nil, "/t", spec, "", nil)
	for i := 0; i < 50; i++ {
		if got := buildClaudeShellCmd("/c", nil, "/t", spec, "", nil); got != first {
			t.Fatalf("output not deterministic\nfirst: %s\ngot:   %s", first, got)
		}
	}
}

func TestBuildClaudeShellCmd_NilWarnOut(t *testing.T) {
	// warnOut=nil must not panic even when there are invalid entries.
	spec := ProcessSpec{
		Name:    "p",
		WebPort: "bad;port",
		Env:     map[string]string{"bad key": "v"},
	}
	_ = buildClaudeShellCmd("/c", nil, "/t", spec, "", nil)
}

func TestSupervisorEnvKeyPatternMatchesConfig(t *testing.T) {
	// Sanity: the defense-in-depth pattern should accept everything
	// config.Validate() accepts, and nothing more permissive.
	good := []string{"FOO", "foo_bar", "_X", "A1", "HTTP_PROXY"}
	bad := []string{"1X", "FOO-BAR", "FOO BAR", "X;rm", "", "FOO="}

	for _, k := range good {
		if !supervisorEnvKeyPattern.MatchString(k) {
			t.Errorf("expected %q to be accepted", k)
		}
	}
	for _, k := range bad {
		if supervisorEnvKeyPattern.MatchString(k) {
			t.Errorf("expected %q to be rejected", k)
		}
	}
}
