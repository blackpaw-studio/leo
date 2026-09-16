package consult

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	rosterMarker               = "@leo_roster_owned"
	rosterStatusMarker         = "@leo_roster_status_owned"
	rosterStatusIntervalMarker = "@leo_roster_status_interval_owned"
	rosterFormat0Marker        = "@leo_roster_format0_owned"
	rosterFormat0ValueMarker   = "@leo_roster_format0_value"
	rosterFormat               = "#[align=left] #{@leo_roster}"
)

type rosterSessionState struct {
	sessionID                                                                                                                                                                        string
	text                                                                                                                                                                             string
	format0Value                                                                                                                                                                     string
	rosterMarked, statusChecked, needsStatus, statusMarked, statusSet, statusIntervalChecked, needsStatusInterval, statusIntervalMarked, statusIntervalSet, formatSet, format0Marked bool
	clearFormat, clearFormat0, clearFormatArray, clearFormat0Marker, clearFormat0ValueMarker                                                                                         bool
	clearRoster, clearStatus, clearStatusMarker, clearStatusInterval, clearStatusIntervalMarker, clearRosterMarker                                                                   bool
}

func (s rosterSessionState) managed() bool {
	return s.rosterMarked || s.statusMarked || s.statusIntervalMarked || s.format0Marked || s.statusSet || s.statusIntervalSet || s.formatSet || s.text != ""
}
func (s rosterSessionState) clearing() bool {
	return s.clearFormat || s.clearFormat0 || s.clearFormatArray || s.clearFormat0Marker || s.clearFormat0ValueMarker || s.clearRoster || s.clearStatus || s.clearStatusMarker || s.clearStatusInterval || s.clearStatusIntervalMarker || s.clearRosterMarker
}

type rosterPane struct{ session, sessionID, window, pane string }
type managedRosterState struct {
	sessionID, text                                                 string
	format0Value                                                    string
	rosterMarked, statusMarked, statusIntervalMarked, format0Marked bool
}

// UpdateRoster resolves live tmux membership, renders per-session rosters, and
// applies session-scoped options. The whole update is serialized independently
// of Dispatcher.mu so an older inventory cannot apply after a newer one.
func (v *Viewer) UpdateRoster(records []Record, now time.Time) {
	if v == nil {
		return
	}
	v.defaults()
	v.rosterMu.Lock()
	defer v.rosterMu.Unlock()
	panes, err := v.rosterInventory()
	if err != nil {
		v.log("inventorying roster windows: %v", err)
		return
	}
	if len(panes) == 0 && !v.rosterZeroLogged {
		v.log("roster: inventory returned zero panes")
		v.rosterZeroLogged = true
	}
	managed, err := v.managedRosterSessions()
	if err != nil {
		v.log("inventorying managed roster sessions: %v", err)
		return
	}

	v.mu.Lock()
	windowIDs := make(map[string]string, len(v.windowIDs))
	for id, window := range v.windowIDs {
		windowIDs[id] = window
	}
	v.mu.Unlock()
	byPane, byWindow, liveSessions := map[string]string{}, map[string]string{}, map[string]string{}
	for _, pane := range panes {
		byPane[pane.pane], byWindow[pane.window], liveSessions[pane.session] = pane.session, pane.session, pane.sessionID
	}
	resolved := make(map[string][]Record)
	unresolved := make([]string, 0)
	eligible := 0
	for _, rec := range records {
		if rec.Kind != "dispatch" {
			unresolved = append(unresolved, rec.ID+":not-dispatch")
			continue
		}
		if !rosterEligible(rec, now) {
			unresolved = append(unresolved, rec.ID+":expired")
			continue
		}
		eligible++
		var session string
		if rec.Mode == ModeInteractive {
			session = byPane[rec.PaneID]
			if session == "" {
				unresolved = append(unresolved, fmt.Sprintf("%s:no-pane %s", rec.ID, rec.PaneID))
			}
		} else {
			if rec.ViewerPaneID != "" {
				session = byPane[rec.ViewerPaneID]
				if session == "" {
					unresolved = append(unresolved, fmt.Sprintf("%s:no-pane %s", rec.ID, rec.ViewerPaneID))
				}
			} else {
				window := rec.ViewerWindowID
				if window == "" {
					window = windowIDs[rec.ID]
				}
				session = byWindow[window]
				if session == "" {
					unresolved = append(unresolved, fmt.Sprintf("%s:no-window %s", rec.ID, window))
				}
			}
		}
		if session != "" {
			resolved[session] = append(resolved[session], rec)
		}
	}
	v.logRosterInventory(len(panes), len(liveSessions), len(records), eligible, resolved, unresolved)
	if v.rosters == nil {
		v.rosters = make(map[string]rosterSessionState)
	}
	for session, found := range managed {
		liveSessions[session] = found.sessionID
		state := v.rosters[session]
		if state.sessionID != "" && state.sessionID != found.sessionID {
			state = rosterSessionState{}
		}
		state.sessionID = found.sessionID
		state.rosterMarked, state.statusMarked, state.statusIntervalMarked, state.format0Marked, state.format0Value, state.text = found.rosterMarked, found.statusMarked, found.statusIntervalMarked, found.format0Marked, found.format0Value, found.text
		if _, ok := resolved[session]; !ok {
			resolved[session] = nil
		}
		v.rosters[session] = state
	}
	for session := range v.rosters {
		if _, ok := liveSessions[session]; !ok {
			delete(v.rosters, session)
			continue
		}
		if _, ok := resolved[session]; !ok {
			resolved[session] = nil
		}
	}
	for session, recs := range resolved {
		state := v.rosters[session]
		if sessionID := liveSessions[session]; state.sessionID != "" && state.sessionID != sessionID {
			state = rosterSessionState{}
		}
		state.sessionID = liveSessions[session]
		text := RenderRoster(recs, now)
		if text == "" {
			wasManaged := state.managed() || state.clearing()
			sessionID := state.sessionID
			if state.managed() || state.clearing() {
				state = v.clearRosterState(session, state)
			}
			if !state.managed() && !state.clearing() {
				delete(v.rosters, session)
				if wasManaged {
					v.logRosterEvent("cleared:"+sessionID, "roster: cleared from %q", session)
				}
			} else {
				v.rosters[session] = state
			}
			continue
		}
		if state.clearing() {
			state = v.clearRosterState(session, state)
			if state.clearing() {
				v.rosters[session] = state
				continue
			}
			state.sessionID = liveSessions[session]
		}
		wasApplied := state.text != ""
		state = v.applyRoster(session, text, state)
		v.rosters[session] = state
		if !wasApplied && state.text != "" {
			v.logRosterEvent("applied:"+state.sessionID, "roster: applied to %q", session)
		}
	}
}

func (v *Viewer) logRosterInventory(panes, sessions, records, eligible int, resolved map[string][]Record, unresolved []string) {
	resolvedParts := make([]string, 0, len(resolved))
	for session, recs := range resolved {
		if len(recs) > 0 {
			resolvedParts = append(resolvedParts, fmt.Sprintf("%s:%d", session, len(recs)))
		}
	}
	sort.Strings(resolvedParts)
	sort.Strings(unresolved)
	line := fmt.Sprintf("roster: inventory panes=%d sessions=%d records=%d eligible=%d resolved=[%s] unresolved=[%s]", panes, sessions, records, eligible, strings.Join(resolvedParts, " "), strings.Join(unresolved, " "))
	if line != v.rosterInventoryLog {
		v.log("%s", line)
		v.rosterInventoryLog = line
	}
}

func (v *Viewer) logRosterEvent(key, format string, args ...any) {
	if v.rosterEvents == nil {
		v.rosterEvents = make(map[string]bool)
	}
	if !v.rosterEvents[key] {
		v.log(format, args...)
		v.rosterEvents[key] = true
	}
}

func (v *Viewer) rosterInventory() ([]rosterPane, error) {
	out, err := v.output("list-panes", "-a", "-F", "#{session_name}\t#{session_id}\t#{window_id}\t#{pane_id}")
	if err != nil {
		return nil, err
	}
	var panes []rosterPane
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) == 4 && parts[0] != "" {
			panes = append(panes, rosterPane{parts[0], parts[1], parts[2], parts[3]})
		}
	}
	return panes, nil
}

func (v *Viewer) managedRosterSessions() (map[string]managedRosterState, error) {
	out, err := v.output("list-sessions", "-F", "#{session_name}\t#{session_id}\t#{@leo_roster_owned}\t#{@leo_roster_status_owned}\t#{@leo_roster_status_interval_owned}\t#{@leo_roster_format0_owned}\t#{@leo_roster}")
	if err != nil {
		return nil, err
	}
	managed := make(map[string]managedRosterState)
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		parts := strings.SplitN(line, "\t", 7)
		if len(parts) >= 5 && (parts[2] == "1" || parts[3] == "1" || parts[4] == "1" || (len(parts) >= 6 && parts[5] == "1")) {
			state := managedRosterState{sessionID: parts[1], rosterMarked: parts[2] == "1", statusMarked: parts[3] == "1", statusIntervalMarked: parts[4] == "1", format0Marked: len(parts) >= 6 && parts[5] == "1"}
			if state.format0Marked {
				value, err := v.output("show-options", "-t", tmux.Target(parts[0])+":", rosterFormat0ValueMarker)
				if err != nil {
					return nil, err
				}
				state.format0Value, err = rosterOptionValue(rosterFormat0ValueMarker, string(value))
				if err != nil {
					return nil, err
				}
			}
			if len(parts) == 7 {
				state.text = parts[6]
			}
			managed[parts[0]] = state
		}
	}
	return managed, nil
}

func rosterOptionValue(option, output string) (string, error) {
	line := strings.TrimSuffix(strings.TrimSuffix(output, "\n"), "\r")
	prefix := option + " "
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("unexpected %s output %q", option, output)
	}
	value := strings.TrimPrefix(line, prefix)
	if strings.HasPrefix(value, `"`) {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("parsing %s: %w", option, err)
		}
		return unquoted, nil
	}
	return value, nil
}

func (v *Viewer) applyRoster(session, text string, state rosterSessionState) rosterSessionState {
	target := tmux.Target(session) + ":"
	if !state.rosterMarked {
		if err := v.run("set-option", "-t", target, rosterMarker, "1"); err != nil {
			v.log("marking roster ownership for %q: %v", session, err)
			return state
		}
		state.rosterMarked = true
	}
	if !state.statusChecked {
		out, err := v.output("show-options", "-A", "-t", target, "-v", "status")
		if err != nil {
			v.log("reading status for %q: %v", session, err)
			return state
		}
		lines, err := rosterStatusLines(string(out))
		if err != nil {
			v.log("parsing status for %q: %v", session, err)
			return state
		}
		state.statusChecked = true
		if lines < 2 {
			state.needsStatus = true
		} else {
			v.logRosterEvent("skip-status:"+state.sessionID, "roster: skipped status for %q: already %d", session, lines)
		}
	}
	if state.needsStatus && !state.statusSet {
		if !state.statusMarked {
			if err := v.run("set-option", "-t", target, rosterStatusMarker, "1"); err != nil {
				v.log("marking status ownership for %q: %v", session, err)
				return state
			}
			state.statusMarked = true
		}
		if err := v.run("set-option", "-t", target, "status", "2"); err != nil {
			v.log("setting status for %q: %v", session, err)
			return state
		}
		state.statusSet = true
	}
	if !state.statusIntervalChecked {
		local, err := v.output("show-options", "-t", target, "-v", "status-interval")
		if err != nil {
			v.log("reading session status-interval for %q: %v", session, err)
			return state
		}
		state.statusIntervalChecked = true
		if strings.TrimSpace(string(local)) == "" {
			out, err := v.output("show-options", "-A", "-t", target, "-v", "status-interval")
			if err != nil {
				v.log("reading effective status-interval for %q: %v", session, err)
				return state
			}
			interval, err := strconv.Atoi(strings.TrimSpace(string(out)))
			if err != nil {
				v.log("parsing status-interval for %q: %v", session, err)
				return state
			}
			state.needsStatusInterval = interval > 1
		}
	}
	if state.needsStatusInterval && !state.statusIntervalMarked {
		if err := v.run("set-option", "-t", target, rosterStatusIntervalMarker, "1"); err != nil {
			v.log("marking status-interval ownership for %q: %v", session, err)
			return state
		}
		state.statusIntervalMarked = true
	}
	if state.needsStatusInterval && !state.statusIntervalSet {
		if err := v.run("set-option", "-t", target, "status-interval", "1"); err != nil {
			v.log("setting status-interval for %q: %v", session, err)
			return state
		}
		state.statusIntervalSet = true
	}
	if !state.formatSet {
		local, err := v.output("show-options", "-t", target, "status-format")
		if err != nil {
			v.log("reading session status-format for %q: %v", session, err)
			return state
		}
		if !statusFormatHasEntries(string(local)) {
			global, err := v.output("show-options", "-g", "-v", "status-format[0]")
			if err != nil {
				v.log("reading global status-format[0] for %q: %v", session, err)
				return state
			}
			if !state.format0Marked {
				if err := v.run("set-option", "-t", target, rosterFormat0Marker, "1"); err != nil {
					v.log("marking status-format[0] ownership for %q: %v", session, err)
					return state
				}
				state.format0Marked = true
			}
			state.format0Value = strings.TrimRight(string(global), "\r\n")
			if err := v.run("set-option", "-t", target, rosterFormat0ValueMarker, state.format0Value); err != nil {
				v.log("recording status-format[0] ownership for %q: %v", session, err)
				return state
			}
			if err := v.run("set-option", "-t", target, "status-format[0]", state.format0Value); err != nil {
				v.log("copying status-format[0] for %q: %v", session, err)
				return state
			}
		}
		if err := v.run("set-option", "-t", target, "status-format[1]", rosterFormat); err != nil {
			v.log("setting roster format for %q: %v", session, err)
			return state
		}
		state.formatSet = true
	}
	if state.text != text {
		if err := v.run("set-option", "-t", target, "@leo_roster", text); err != nil {
			v.log("updating roster for %q: %v", session, err)
			return state
		}
		state.text = text
	}
	return state
}

func rosterStatusLines(value string) (int, error) {
	switch strings.TrimSpace(value) {
	case "on":
		return 1, nil
	case "off":
		return 0, nil
	default:
		return strconv.Atoi(strings.TrimSpace(value))
	}
}

func (v *Viewer) clearRosterState(session string, state rosterSessionState) rosterSessionState {
	target := tmux.Target(session) + ":"
	if !state.clearing() {
		state.clearFormat, state.clearFormatArray, state.clearRoster = true, true, true
		state.clearFormat0, state.clearFormat0Marker, state.clearFormat0ValueMarker = state.format0Marked, state.format0Marked, state.format0Marked
		state.clearStatus, state.clearStatusMarker = state.statusMarked, state.statusMarked
		state.clearStatusInterval, state.clearStatusIntervalMarker = state.statusIntervalMarked, state.statusIntervalMarked
		state.clearRosterMarker = state.rosterMarked
	}
	unset := func(pending *bool, option string) {
		if !*pending {
			return
		}
		if err := v.run("set-option", "-u", "-t", target, option); err != nil {
			v.log("clearing %s for %q: %v", option, session, err)
			return
		}
		*pending = false
	}
	if state.clearFormat {
		if err := v.run("set-option", "-u", "-t", target, "status-format[1]"); err != nil {
			v.log("clearing status-format[1] for %q: %v", session, err)
		} else {
			state.clearFormat = false
		}
	}
	if !state.clearFormat && state.clearFormat0 {
		current, err := v.output("show-options", "-t", target, "-v", "status-format[0]")
		switch {
		case err != nil:
			v.log("reading owned status-format[0] for %q: %v", session, err)
		case strings.TrimRight(string(current), "\r\n") != state.format0Value:
			state.clearFormat0, state.clearFormatArray = false, false
		default:
			if err := v.run("set-option", "-u", "-t", target, "status-format[0]"); err != nil {
				v.log("clearing owned status-format[0] for %q: %v", session, err)
			} else {
				state.clearFormat0 = false
			}
		}
	}
	if !state.clearFormat && !state.clearFormat0 && state.clearFormatArray {
		if out, err := v.output("show-options", "-t", target, "status-format"); err != nil {
			v.log("reading status-format during cleanup for %q: %v", session, err)
		} else if statusFormatHasEntries(string(out)) {
			state.clearFormatArray = false
		} else if err := v.run("set-option", "-u", "-t", target, "status-format"); err != nil {
			v.log("clearing empty status-format for %q: %v", session, err)
		} else {
			state.clearFormatArray = false
		}
	}
	unset(&state.clearRoster, "@leo_roster")
	if state.clearStatus {
		out, err := v.output("show-options", "-A", "-t", target, "-v", "status")
		switch {
		case err != nil:
			v.log("reading status during cleanup for %q: %v", session, err)
		case strings.TrimSpace(string(out)) != "2":
			state.clearStatus = false
		default:
			unset(&state.clearStatus, "status")
		}
	}
	if state.clearStatusInterval {
		local, err := v.output("show-options", "-t", target, "-v", "status-interval")
		switch {
		case err != nil:
			v.log("reading owned status-interval for %q: %v", session, err)
		case strings.TrimSpace(string(local)) != "1":
			state.clearStatusInterval = false
		default:
			unset(&state.clearStatusInterval, "status-interval")
		}
	}
	// Ownership must stay discoverable across a daemon crash until every
	// resource protected by that marker has been cleared successfully.
	if !state.clearStatus {
		unset(&state.clearStatusMarker, rosterStatusMarker)
	}
	if !state.clearStatusInterval {
		unset(&state.clearStatusIntervalMarker, rosterStatusIntervalMarker)
	}
	if !state.clearFormat && !state.clearRoster {
		unset(&state.clearRosterMarker, rosterMarker)
	}
	if !state.clearFormat0 && !state.clearFormatArray {
		unset(&state.clearFormat0Marker, rosterFormat0Marker)
		unset(&state.clearFormat0ValueMarker, rosterFormat0ValueMarker)
	}
	if !state.clearing() {
		return rosterSessionState{}
	}
	return state
}

func statusFormatHasEntries(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "status-format[") {
			return true
		}
	}
	return false
}
