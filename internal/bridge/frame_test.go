package bridge

import "testing"

func TestFramedNamesTheSender(t *testing.T) {
	cases := []struct{ from, text, want string }{
		{"agent alpha", "hi there", "From agent alpha via leo:\n\nhi there"},
		{"orchestrator", "line one\nline two", "From orchestrator via leo:\n\nline one\nline two"},
		{"", "hi", "From an unnamed sender via leo:\n\nhi"},
	}
	for _, tc := range cases {
		if got := Framed(tc.from, tc.text); got != tc.want {
			t.Errorf("Framed(%q, %q) = %q, want %q", tc.from, tc.text, got, tc.want)
		}
	}
}

// pluginWrapped is turn.start's text, observed live from Claude Code
// v2.1.289, for a prompt the leo-bridge mod submits without asUser.
func pluginWrapped(text string) string {
	return "The leo-bridge plugin sent a message:\n" + text +
		"\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — it starts this turn in the user's place. Address the message above."
}

func TestUnwrapPluginPromptRecoversWhatLeoSent(t *testing.T) {
	sent := Framed("orchestrator", "next step\n\nwith a paragraph")
	cases := []struct{ name, in, want string }{
		{"leo-bridge envelope", pluginWrapped(sent), sent},
		{"envelope phrase inside the message", pluginWrapped(sent + "\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns"), sent + "\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns"},
		{"a user's own prompt", "status please", "status please"},
		{"another plugin's envelope", "The other plugin sent a message:\nhi\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — x", "The other plugin sent a message:\nhi\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — x"},
		{"head without Claude's tail", "The leo-bridge plugin sent a message:\nhi", "The leo-bridge plugin sent a message:\nhi"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnwrapPluginPrompt(tc.in); got != tc.want {
				t.Errorf("UnwrapPluginPrompt(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
