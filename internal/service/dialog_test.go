package service

import "testing"

func TestStartupDialogKey(t *testing.T) {
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
			"numbered menu without chrome still declined",
			"  Pick a theme\n  ❯ 1. Dark\n    2. Light\n",
			"Escape",
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
			if got := startupDialogKey(c.pane); got != c.want {
				t.Fatalf("startupDialogKey = %q, want %q", got, c.want)
			}
		})
	}
}
