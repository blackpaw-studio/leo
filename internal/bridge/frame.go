package bridge

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
