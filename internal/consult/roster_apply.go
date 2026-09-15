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
	rosterMarker       = "@leo_roster_owned"
	rosterStatusMarker = "@leo_roster_status_owned"
	rosterFormat       = "#[align=left] #{@leo_roster}"
)

type rosterSessionState struct {
	sessionID                                                                    string
	text                                                                         string
	rosterMarked, statusChecked, needsStatus, statusMarked, statusSet, formatSet bool
	clearFormat, clearRoster, clearStatus, clearStatusMarker, clearRosterMarker  bool
}

func (s rosterSessionState) managed() bool {
	return s.rosterMarked || s.statusMarked || s.statusSet || s.formatSet || s.text != ""
}
func (s rosterSessionState) clearing() bool {
	return s.clearFormat || s.clearRoster || s.clearStatus || s.clearStatusMarker || s.clearRosterMarker
}

type rosterPane struct{ session, sessionID, window, pane string }
type managedRosterState struct {
	sessionID, text            string
	rosterMarked, statusMarked bool
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
		if rec.Status.Terminal() && !rec.EndedAt.IsZero() && !now.Before(rec.EndedAt.Add(viewerGraceAfterEnd)) {
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
			window := rec.ViewerWindowID
			if window == "" {
				window = windowIDs[rec.ID]
			}
			session = byWindow[window]
			if session == "" {
				unresolved = append(unresolved, fmt.Sprintf("%s:no-window %s", rec.ID, window))
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
		state.rosterMarked, state.statusMarked, state.text = found.rosterMarked, found.statusMarked, found.text
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
	out, err := v.output("list-sessions", "-F", "#{session_name}\t#{session_id}\t#{@leo_roster_owned}\t#{@leo_roster_status_owned}\t#{@leo_roster}")
	if err != nil {
		return nil, err
	}
	managed := make(map[string]managedRosterState)
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) >= 4 && (parts[2] == "1" || parts[3] == "1") {
			state := managedRosterState{sessionID: parts[1], rosterMarked: parts[2] == "1", statusMarked: parts[3] == "1"}
			if len(parts) == 5 {
				state.text = parts[4]
			}
			managed[parts[0]] = state
		}
	}
	return managed, nil
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
	if !state.formatSet {
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
		state.clearFormat, state.clearRoster = true, true
		state.clearStatus, state.clearStatusMarker = state.statusMarked, state.statusMarked
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
		} else if out, err := v.output("show-options", "-t", target, "status-format"); err != nil {
			v.log("reading status-format during cleanup for %q: %v", session, err)
		} else if statusFormatHasEntries(string(out)) {
			state.clearFormat = false
		} else if err := v.run("set-option", "-u", "-t", target, "status-format"); err != nil {
			v.log("clearing empty status-format for %q: %v", session, err)
		} else {
			state.clearFormat = false
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
	// Ownership must stay discoverable across a daemon crash until every
	// resource protected by that marker has been cleared successfully.
	if !state.clearStatus {
		unset(&state.clearStatusMarker, rosterStatusMarker)
	}
	if !state.clearFormat && !state.clearRoster {
		unset(&state.clearRosterMarker, rosterMarker)
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
