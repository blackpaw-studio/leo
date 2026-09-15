package consult

import "fmt"

func rosterUsage(rec Record) string {
	parts := make([]string, 0, 2)
	partial := ""
	if rec.UsageIncomplete {
		partial = "~"
	}
	if rec.ToolCalls != nil {
		label := "tools"
		if *rec.ToolCalls == 1 {
			label = "tool"
		}
		parts = append(parts, fmt.Sprintf("%d%s %s", *rec.ToolCalls, partial, label))
	}
	if rec.InputTokens != nil && rec.OutputTokens != nil {
		parts = append(parts, fmt.Sprintf("%.1fk%s tokens", (float64(*rec.InputTokens)+float64(*rec.OutputTokens))/1000, partial))
	}
	if len(parts) == 0 && rec.UsageIncomplete {
		parts = append(parts, "partial")
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + joinUsage(parts)
}

func joinUsage(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	out := parts[0]
	for _, part := range parts[1:] {
		out += " · " + part
	}
	return out
}
