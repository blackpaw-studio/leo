package consult

import "fmt"

func rosterUsage(rec Record) string {
	parts := make([]string, 0, 2)
	if rec.ToolCalls != nil {
		parts = append(parts, fmt.Sprintf("%d tools", *rec.ToolCalls))
	}
	if rec.InputTokens != nil && rec.OutputTokens != nil {
		parts = append(parts, fmt.Sprintf("%.1fk tokens", float64(*rec.InputTokens+*rec.OutputTokens)/1000))
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
