package service

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// TestSessionEnvArgs covers the env that reaches a supervised process. Env is
// passed as tmux `-e KEY=VALUE` argv elements rather than interpolated into
// the shell command, so values never land in the session's start command —
// see the sessionEnvArgs doc comment.
func TestSessionEnvArgs(t *testing.T) {
	const validToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	tests := []struct {
		name        string
		spec        ProcessSpec
		wantContain []string
		wantMissing []string
		wantWarns   []string
	}{
		{
			name: "clean env map passes through",
			spec: ProcessSpec{
				Name:    "alpha",
				WebPort: "8370",
				Env:     map[string]string{"FOO": "bar"},
			},
			wantContain: []string{
				"FOO=bar",
				"LEO_PROCESS_NAME=alpha",
				"LEO_WEB_PORT=8370",
			},
		},
		{
			name: "malicious env key is dropped",
			spec: ProcessSpec{
				Name: "alpha",
				Env: map[string]string{
					"GOOD":       "ok",
					"X;rm -rf /": "y",
				},
			},
			wantContain: []string{"GOOD=ok"},
			wantMissing: []string{"X;rm -rf /=y"},
			wantWarns:   []string{`dropping invalid env key "X;rm -rf /"`},
		},
		{
			name: "shell metacharacters in a value are inert",
			spec: ProcessSpec{
				Name: "alpha",
				Env:  map[string]string{"X": "$(whoami)"},
			},
			// argv elements are not shell-parsed, so the value needs no
			// quoting and arrives at the process byte-identical.
			wantContain: []string{"X=$(whoami)"},
		},
		{
			name: "value with a single quote survives unmangled",
			spec: ProcessSpec{
				Name: "alpha",
				Env:  map[string]string{"X": "it's evil"},
			},
			wantContain: []string{"X=it's evil"},
		},
		{
			name:        "invalid WebPort is rejected",
			spec:        ProcessSpec{Name: "alpha", WebPort: "80; rm -rf"},
			wantMissing: []string{"LEO_WEB_PORT", "80; rm -rf"},
			wantWarns:   []string{`dropping invalid LEO_WEB_PORT "80; rm -rf"`},
		},
		{
			name:        "empty WebPort is omitted without a warning",
			spec:        ProcessSpec{Name: "alpha"},
			wantMissing: []string{"LEO_WEB_PORT"},
		},
		{
			name:        "valid WebToken becomes LEO_API_TOKEN",
			spec:        ProcessSpec{Name: "alpha", WebToken: validToken},
			wantContain: []string{"LEO_API_TOKEN=" + validToken},
		},
		{
			name:        "malformed WebToken is dropped and warned",
			spec:        ProcessSpec{Name: "alpha", WebToken: "short-not-hex"},
			wantMissing: []string{"LEO_API_TOKEN", "short-not-hex"},
			wantWarns:   []string{"dropping malformed LEO_API_TOKEN"},
		},
		{
			// tmux ignores `-e PATH=…` — the pane's PATH comes from the
			// new-session client's environment — so listing it here would
			// assert a guarantee tmux does not honour. "PATH=" alone would
			// match inside LEO_TMUX_PATH=…; \x00 is the element separator
			// used by the joined form below, so this asserts no arg *starts*
			// with PATH=.
			name:        "PATH is never passed as session env",
			spec:        ProcessSpec{Name: "alpha", Env: map[string]string{"PATH": "/should/not/appear"}},
			wantMissing: []string{"\x00PATH="},
			wantWarns:   []string{"ignoring configured PATH"},
		},
		{
			name:        "keys with a leading digit are rejected",
			spec:        ProcessSpec{Name: "alpha", Env: map[string]string{"1BAD": "oops", "OK": "fine"}},
			wantContain: []string{"OK=fine"},
			wantMissing: []string{"1BAD=oops"},
			wantWarns:   []string{`dropping invalid env key "1BAD"`},
		},
		{
			name:        "empty key is rejected",
			spec:        ProcessSpec{Name: "alpha", Env: map[string]string{"": "oops", "OK": "fine"}},
			wantContain: []string{"OK=fine"},
			wantMissing: []string{"=oops"},
			wantWarns:   []string{`dropping invalid env key ""`},
		},
		{
			// Leo's own vars are authoritative: a process env that tries to
			// shadow LEO_PROCESS_NAME must lose, as it did when leo's exports
			// ran last in the old shell-string form.
			name:        "leo vars win over a colliding process env key",
			spec:        ProcessSpec{Name: "alpha", Env: map[string]string{"LEO_PROCESS_NAME": "impostor"}},
			wantContain: []string{"LEO_PROCESS_NAME=alpha"},
			wantMissing: []string{"LEO_PROCESS_NAME=impostor"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var warn bytes.Buffer
			got := sessionEnvArgs("/usr/local/bin/tmux", tt.spec, &warn)

			// Every entry must be a "-e" flag followed by KEY=VALUE.
			for i := 0; i < len(got); i += 2 {
				if got[i] != "-e" {
					t.Fatalf("arg %d = %q, want -e; full args: %v", i, got[i], got)
				}
			}
			joined := strings.Join(got, "\x00")
			for _, want := range tt.wantContain {
				if !slices.Contains(got, want) {
					t.Errorf("env args missing %q; got %v", want, got)
				}
			}
			for _, unwanted := range tt.wantMissing {
				if strings.Contains(joined, unwanted) {
					t.Errorf("env args should not contain %q; got %v", unwanted, got)
				}
			}
			warnStr := warn.String()
			for _, w := range tt.wantWarns {
				if !strings.Contains(warnStr, w) {
					t.Errorf("expected warning %q, got: %q", w, warnStr)
				}
			}
			if len(tt.wantWarns) == 0 && warnStr != "" {
				t.Errorf("expected no warnings, got: %q", warnStr)
			}
		})
	}
}

// TestSessionEnvArgsDeterministicOrder keeps logs and tests readable: Go map
// iteration is randomized, so the args must be sorted.
func TestSessionEnvArgsDeterministicOrder(t *testing.T) {
	spec := ProcessSpec{
		Name:    "p",
		WebPort: "8370",
		Env:     map[string]string{"ZETA": "z", "ALPHA": "a", "MIKE": "m"},
	}
	first := sessionEnvArgs("/t", spec, nil)
	for i := 0; i < 50; i++ {
		if got := sessionEnvArgs("/t", spec, nil); !slices.Equal(got, first) {
			t.Fatalf("output not deterministic\nfirst: %v\ngot:   %v", first, got)
		}
	}
}

// TestBuildClaudeShellCmdCarriesNoEnv is the leak guard. tmux persists a
// pane's start command (`list-panes -F '#{pane_start_command}'`) and it shows
// up in ps output, so any credential interpolated into this string is
// readable for the life of the session by anything running as the same user.
// Env belongs in sessionEnvArgs, never here.
func TestBuildClaudeShellCmdCarriesNoEnv(t *testing.T) {
	const secret = "ops_totally_fake_token_do_not_use"
	spec := ProcessSpec{
		Name:     "alpha",
		WebPort:  "8370",
		WebToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Env:      map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": secret},
	}
	got := buildClaudeShellCmd("/usr/local/bin/claude", []string{"--model", "sonnet"}, spec, "")

	for _, unwanted := range []string{secret, "OP_SERVICE_ACCOUNT_TOKEN", "LEO_API_TOKEN", "export "} {
		if strings.Contains(got, unwanted) {
			t.Errorf("shell command contains %q; env must ride in tmux -e args\nfull cmd: %s", unwanted, got)
		}
	}
	if !strings.Contains(got, "'--model'") || !strings.Contains(got, "'sonnet'") {
		t.Errorf("shell command lost the claude args: %s", got)
	}
}

// TestBuildClaudeShellCmdExportsPath pins PATH precedence. tmux runs the pane
// command through $SHELL -c, so a PATH set in the user's shell rc (~/.zshenv,
// which zsh sources for non-interactive shells too) runs BEFORE the command
// and would otherwise win. The inline export runs after rc files, which is
// how leo's PATH has always reached agents. PATH is not a credential, so it
// stays in the command string even though env moved to tmux -e args.
func TestBuildClaudeShellCmdExportsPath(t *testing.T) {
	spec := ProcessSpec{Name: "alpha"}
	got := buildClaudeShellCmd("/c", []string{"--model", "sonnet"}, spec, "/usr/bin:/bin")

	if !strings.Contains(got, "export PATH='/usr/bin:/bin';") {
		t.Errorf("cmd missing inline PATH export\nfull cmd: %s", got)
	}
	// The export must precede claude, or the rc-file PATH wins.
	if strings.Index(got, "export PATH=") > strings.Index(got, "'/c'") {
		t.Errorf("PATH export must come before the binary\nfull cmd: %s", got)
	}
	if empty := buildClaudeShellCmd("/c", nil, spec, ""); strings.Contains(empty, "export PATH=") {
		t.Errorf("empty PATH should emit no export\nfull cmd: %s", empty)
	}
}

func TestBuildClaudeShellCmd_ArgsAreShellQuoted(t *testing.T) {
	spec := ProcessSpec{Name: "alpha"}
	got := buildClaudeShellCmd("/usr/local/bin/claude", []string{"--append-system-prompt", "hello $USER"}, spec, "")

	for _, want := range []string{"'--append-system-prompt'", "'hello $USER'"} {
		if !strings.Contains(got, want) {
			t.Errorf("cmd missing %q\nfull cmd: %s", want, got)
		}
	}
	if strings.Contains(got, " hello $USER ") {
		t.Errorf("unquoted $USER would be shell-expanded\nfull cmd: %s", got)
	}
}

func TestBuildClaudeShellCmd_ExitCapture(t *testing.T) {
	spec := ProcessSpec{
		Name:     "assistant",
		StateDir: "/var/leo/state",
	}
	got := buildClaudeShellCmd("/c", []string{"--model", "sonnet"}, spec, "")

	wantSubstrings := []string{
		"2> '/var/leo/state/assistant-stderr.log'",
		"ec=$?",
		"echo \"$ec\" > '/var/leo/state/assistant-exit.code'",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(got, sub) {
			t.Errorf("cmd missing %q\nfull cmd: %s", sub, got)
		}
	}
}

func TestBuildClaudeShellCmd_NoExitCaptureWhenStateDirMissing(t *testing.T) {
	spec := ProcessSpec{Name: "p"} // StateDir empty
	got := buildClaudeShellCmd("/c", []string{"--model", "sonnet"}, spec, "")
	for _, sub := range []string{"-stderr.log", "-exit.code", "ec=$?"} {
		if strings.Contains(got, sub) {
			t.Errorf("cmd should not contain %q when StateDir is empty\nfull cmd: %s", sub, got)
		}
	}
}

func TestSessionEnvArgs_NilWarnOut(t *testing.T) {
	// warnOut=nil must not panic even when there are invalid entries.
	spec := ProcessSpec{
		Name:    "p",
		WebPort: "bad;port",
		Env:     map[string]string{"bad key": "v"},
	}
	_ = sessionEnvArgs("/t", spec, nil)
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
