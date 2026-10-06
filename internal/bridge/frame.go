package bridge

import "strings"

// Framed prefixes text with a line naming who sent it, for a deliver the
// model must not mistake for its user's own words (a message from another
// agent, a dispatch orchestrator's follow-up). from reads naturally after
// "From ": "agent alpha", "orchestrator".
func Framed(from, text string) string {
	if from == "" {
		from = "an unnamed sender"
	}
	return "From " + from + " via leo:\n\n" + text
}

// ModName is the plugin name the leo-bridge mod loads under; Claude Code
// names it when it shows a prompt the mod submitted.
const ModName = "leo-bridge"

// pluginPromptHead and pluginPromptTail enclose a prompt the mod submits
// without as_user, as Claude Code (v2.1.289) shows it and reports it in
// turn.start: "The leo-bridge plugin sent a message:\n<text>\n\nThis is how
// Claude Code surfaces a prompt a plugin submits between turns — …".
const (
	pluginPromptHead = "The " + ModName + " plugin sent a message:\n"
	pluginPromptTail = "\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns"
)

// UnwrapPluginPrompt returns the text leo delivered from a turn.start prompt
// Claude wrapped as a plugin's message, so the turn can be matched to what
// was sent. Anything else — a user's prompt, another plugin's, a wording
// this does not recognise — is returned unchanged.
func UnwrapPluginPrompt(text string) string {
	rest, ok := strings.CutPrefix(text, pluginPromptHead)
	if !ok {
		return text
	}
	i := strings.LastIndex(rest, pluginPromptTail)
	if i < 0 {
		return text
	}
	return rest[:i]
}
