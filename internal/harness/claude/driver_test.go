package claude

import (
	"context"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestTmuxTUIDriverStyle(t *testing.T) {
	if got := (Claude{}).Driver().Style(); got != harness.DriveTmux {
		t.Fatalf("Style() = %q, want %q", got, harness.DriveTmux)
	}
}

func TestTmuxTUIDriverStartIsNoOp(t *testing.T) {
	if err := (Claude{}).Driver().Start(context.Background(), harness.SessionHandle{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestTmuxTUIDriverInjectDelegatesToInjectPrompt(t *testing.T) {
	var gotSession, gotBody string
	restore := SetInjectPromptForTest(func(ctx context.Context, tmuxPath, session, body string) error {
		gotSession, gotBody = session, body
		return nil
	})
	defer restore()
	res, err := (Claude{}).Driver().Inject(context.Background(), harness.SessionHandle{TmuxSession: "leo-x"}, "hello")
	if err != nil || res != nil {
		t.Fatalf("Inject = (%v, %v), want (nil, nil)", res, err)
	}
	if gotSession != "leo-x" || gotBody != "hello" {
		t.Fatalf("delegated (%q, %q)", gotSession, gotBody)
	}
}

func TestTmuxTUIDriverPaneKey(t *testing.T) {
	d := TmuxTUIDriver{}
	cases := []struct {
		name string
		pane string
		want string
	}{
		{
			"resume-from-summary accepted with Enter",
			"  Resume from summary?\n  Press Enter to confirm\n",
			"Enter",
		},
		{
			"fullscreen renderer announcement declined with Escape",
			"  Try the new fullscreen renderer?\n  ❯ 1. Yes, try it\n    2. Not now\n  Enter to confirm · Esc to cancel\n",
			"Escape",
		},
		{
			// A numbered menu with NO confirm/cancel footer is visually
			// indistinguishable from ordinary numbered output, so it must be
			// left untouched — never auto-escaped into live work.
			"numbered menu without chrome is left alone",
			"  Pick a theme\n  ❯ 1. Dark\n    2. Light\n",
			"",
		},
		{
			// Regression for the release that fired Escape into every session
			// every poll: a plain numbered list in normal agent output must
			// never be treated as a dialog.
			"ordinary numbered-list output does nothing",
			"Here's my plan:\n  1. Read the file\n  2. Edit it\n  3. Run tests\n❯ \n",
			"",
		},
		{
			"trust prompt left for a human",
			"  Do you trust the files in this folder?\n  ❯ 1. Yes, proceed\n    2. No, exit\n  Enter to confirm · Esc to cancel\n",
			"",
		},
		{
			"permission dialog left alone",
			"  Grant permission to run this tool?\n  ❯ 1. Allow\n  Esc to cancel\n",
			"",
		},
		{"plain empty prompt does nothing", "──────\n❯ \n──────\n", ""},
		{"ordinary output does nothing", "doing some work...\nstill working\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := d.PaneKey(c.pane); got != c.want {
				t.Fatalf("PaneKey = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStripResumeArg(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "removes resume and value",
			args: []string{"--add-dir", "/workspace", "--resume", "abc-123", "--model", "sonnet"},
			want: []string{"--add-dir", "/workspace", "--model", "sonnet"},
		},
		{
			name: "no resume present",
			args: []string{"--add-dir", "/workspace", "--session-id", "abc-123"},
			want: []string{"--add-dir", "/workspace", "--session-id", "abc-123"},
		},
		{
			name: "resume at end without value",
			args: []string{"--add-dir", "/workspace", "--resume"},
			want: []string{"--add-dir", "/workspace", "--resume"},
		},
		{
			name: "empty args",
			args: []string{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripResumeArg(tt.args)
			if len(got) != len(tt.want) {
				t.Errorf("stripResumeArg(%v) = %v, want %v", tt.args, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("stripResumeArg(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHasResumeArg(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"present", []string{"--model", "sonnet", "--resume", "abc"}, true},
		{"absent", []string{"--model", "sonnet", "--session-id", "abc"}, false},
		{"trailing without value", []string{"--model", "sonnet", "--resume"}, false},
		{"empty", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasResumeArg(tt.args); got != tt.want {
				t.Errorf("hasResumeArg(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestHasSessionIDArg(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"present", []string{"--model", "sonnet", "--session-id", "abc"}, true},
		{"absent", []string{"--model", "sonnet", "--resume", "abc"}, false},
		{"trailing without value", []string{"--model", "sonnet", "--session-id"}, false},
		{"empty", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasSessionIDArg(tt.args); got != tt.want {
				t.Errorf("hasSessionIDArg(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestConvertSessionIDToResume(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "rewrites session-id to resume",
			args: []string{"--add-dir", "/ws", "--session-id", "abc-123", "--model", "sonnet"},
			want: []string{"--add-dir", "/ws", "--resume", "abc-123", "--model", "sonnet"},
		},
		{
			name: "leaves existing resume untouched",
			args: []string{"--resume", "abc-123", "--model", "sonnet"},
			want: []string{"--resume", "abc-123", "--model", "sonnet"},
		},
		{
			name: "no session selection flag",
			args: []string{"--add-dir", "/ws", "--model", "sonnet"},
			want: []string{"--add-dir", "/ws", "--model", "sonnet"},
		},
		{
			name: "session-id at end without value is left as-is",
			args: []string{"--add-dir", "/ws", "--session-id"},
			want: []string{"--add-dir", "/ws", "--session-id"},
		},
		{
			name: "empty args",
			args: []string{},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertSessionIDToResume(tt.args)
			if len(got) != len(tt.want) {
				t.Errorf("convertSessionIDToResume(%v) = %v, want %v", tt.args, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("convertSessionIDToResume(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRecoverQuickExitLadder(t *testing.T) {
	d := TmuxTUIDriver{}
	tests := []struct {
		name     string
		args     []string
		wantArgs []string
		wantAct  harness.QuickExitAction
	}{
		{"session-id converts to resume", []string{"--model", "opus", "--session-id", "abc"}, []string{"--model", "opus", "--resume", "abc"}, harness.QuickExitRetryArgs},
		{"resume strips and clears", []string{"--model", "opus", "--resume", "abc"}, []string{"--model", "opus"}, harness.QuickExitClearAndNoResume},
		{"plain args clear session", []string{"--model", "opus"}, []string{"--model", "opus"}, harness.QuickExitClearSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotAct := d.RecoverQuickExit(tt.args)
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) || gotAct != tt.wantAct {
				t.Errorf("got (%v, %d), want (%v, %d)", gotArgs, gotAct, tt.wantArgs, tt.wantAct)
			}
		})
	}
}
