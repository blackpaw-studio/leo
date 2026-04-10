package web

import (
	"fmt"
	"strconv"
	"strings"
)

// describeCron converts a 5-field cron expression to a human-readable string.
// Examples:
//
//	"0 9 * * *"       → "Daily at 9:00 AM"
//	"*/15 * * * *"    → "Every 15 minutes"
//	"0,30 7-21 * * *" → "At :00, :30 past hours 7–21"
//	"30 10 * * 1-5"   → "Weekdays at 10:30 AM"
func describeCron(expr string) string {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return expr
	}

	minute, hour, dom, month, dow := fields[0], fields[1], fields[2], fields[3], fields[4]

	// Every minute
	if minute == "*" && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		return "Every minute"
	}

	// Step-based minute: */N
	if strings.HasPrefix(minute, "*/") && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		n := strings.TrimPrefix(minute, "*/")
		return fmt.Sprintf("Every %s minutes", n)
	}

	// Step-based hour: 0 */N
	if minute == "0" && strings.HasPrefix(hour, "*/") && dom == "*" && month == "*" && dow == "*" {
		n := strings.TrimPrefix(hour, "*/")
		return fmt.Sprintf("Every %s hours", n)
	}

	// Build time description
	timeDesc := describeTime(minute, hour)

	// Build day filter
	dayDesc := describeDays(dom, month, dow)

	if dayDesc == "" {
		return timeDesc
	}
	return dayDesc + " " + timeDesc
}

func describeTime(minute, hour string) string {
	if hour == "*" {
		// Multiple minutes per hour
		if strings.Contains(minute, ",") {
			parts := strings.Split(minute, ",")
			formatted := make([]string, len(parts))
			for i, p := range parts {
				formatted[i] = ":" + padMinute(p)
			}
			return "at " + strings.Join(formatted, ", ") + " past every hour"
		}
		if strings.HasPrefix(minute, "*/") {
			return fmt.Sprintf("every %s minutes", strings.TrimPrefix(minute, "*/"))
		}
		return "at :" + padMinute(minute) + " every hour"
	}

	// Specific hour(s)
	if strings.Contains(hour, ",") || strings.Contains(hour, "-") || strings.Contains(hour, "/") {
		hourDesc := describeHourRange(hour)
		minDesc := describeMinuteList(minute)
		return fmt.Sprintf("at %s, %s", minDesc, hourDesc)
	}

	// Single hour + minute(s)
	h, err := strconv.Atoi(hour)
	if err != nil {
		return fmt.Sprintf("at %s:%s", hour, padMinute(minute))
	}

	if strings.Contains(minute, ",") {
		parts := strings.Split(minute, ",")
		times := make([]string, len(parts))
		for i, p := range parts {
			times[i] = formatTime(h, p)
		}
		return "at " + joinNatural(times)
	}

	return "at " + formatTime(h, minute)
}

func describeDays(dom, month, dow string) string {
	var parts []string

	if dow != "*" {
		parts = append(parts, describeDOW(dow))
	}

	if dom != "*" {
		parts = append(parts, describeDayOfMonth(dom))
	}

	if month != "*" {
		parts = append(parts, describeMonth(month))
	}

	if len(parts) == 0 {
		return "Daily"
	}
	return strings.Join(parts, ", ")
}

func describeDOW(dow string) string {
	dayNames := map[string]string{
		"0": "Sun", "1": "Mon", "2": "Tue", "3": "Wed",
		"4": "Thu", "5": "Fri", "6": "Sat", "7": "Sun",
	}

	if dow == "1-5" {
		return "Weekdays"
	}
	if dow == "0,6" || dow == "6,0" {
		return "Weekends"
	}

	if strings.Contains(dow, ",") {
		days := strings.Split(dow, ",")
		names := make([]string, len(days))
		for i, d := range days {
			if n, ok := dayNames[d]; ok {
				names[i] = n
			} else {
				names[i] = d
			}
		}
		return strings.Join(names, ", ")
	}

	if strings.Contains(dow, "-") {
		parts := strings.SplitN(dow, "-", 2)
		from := dayNames[parts[0]]
		to := dayNames[parts[1]]
		if from == "" {
			from = parts[0]
		}
		if to == "" {
			to = parts[1]
		}
		return from + "–" + to
	}

	if n, ok := dayNames[dow]; ok {
		return n
	}
	return dow
}

func describeDayOfMonth(dom string) string {
	if strings.Contains(dom, ",") {
		return "on days " + dom
	}
	if strings.Contains(dom, "-") {
		return "days " + strings.Replace(dom, "-", "–", 1)
	}
	return "on day " + dom
}

func describeMonth(month string) string {
	monthNames := map[string]string{
		"1": "Jan", "2": "Feb", "3": "Mar", "4": "Apr",
		"5": "May", "6": "Jun", "7": "Jul", "8": "Aug",
		"9": "Sep", "10": "Oct", "11": "Nov", "12": "Dec",
	}
	if n, ok := monthNames[month]; ok {
		return "in " + n
	}
	return "month " + month
}

func describeHourRange(hour string) string {
	if strings.Contains(hour, "-") {
		parts := strings.SplitN(hour, "-", 2)
		from, _ := strconv.Atoi(parts[0])
		to, _ := strconv.Atoi(parts[1])
		return fmt.Sprintf("%s–%s", formatHour(from), formatHour(to))
	}
	if strings.Contains(hour, ",") {
		parts := strings.Split(hour, ",")
		formatted := make([]string, len(parts))
		for i, p := range parts {
			h, _ := strconv.Atoi(p)
			formatted[i] = formatHour(h)
		}
		return strings.Join(formatted, ", ")
	}
	return hour
}

func describeMinuteList(minute string) string {
	if strings.Contains(minute, ",") {
		parts := strings.Split(minute, ",")
		formatted := make([]string, len(parts))
		for i, p := range parts {
			formatted[i] = ":" + padMinute(p)
		}
		return strings.Join(formatted, " and ")
	}
	return ":" + padMinute(minute)
}

func formatTime(h int, minute string) string {
	m, err := strconv.Atoi(minute)
	if err != nil {
		return fmt.Sprintf("%d:%s", h, padMinute(minute))
	}

	suffix := "AM"
	displayH := h
	if h == 0 {
		displayH = 12
	} else if h == 12 {
		suffix = "PM"
	} else if h > 12 {
		displayH = h - 12
		suffix = "PM"
	}

	return fmt.Sprintf("%d:%02d %s", displayH, m, suffix)
}

func formatHour(h int) string {
	suffix := "AM"
	displayH := h
	if h == 0 {
		displayH = 12
	} else if h == 12 {
		suffix = "PM"
	} else if h > 12 {
		displayH = h - 12
		suffix = "PM"
	}
	return fmt.Sprintf("%d %s", displayH, suffix)
}

func padMinute(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

func joinNatural(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}
