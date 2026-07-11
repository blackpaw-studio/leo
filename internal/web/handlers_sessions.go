package web

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// sessionRow is one configured persistent session with live runtime status
// attached: the stored --resume session id, whether its tmux session is
// currently alive, and its router-reported queue depth.
type sessionRow struct {
	Name     string
	StoredID string
	TmuxLive bool
	Depth    int // queued + in-flight; -1 when unknown (no SessionRuntimeProvider wired up)
	Form     formData
}

// sessionsPageData feeds page_sessions.
type sessionsPageData struct {
	Sessions []sessionRow
}

// buildSessionsData assembles the schema-driven Sessions page: one card per
// cfg.Sessions entry with its config form plus live status pulled from the
// session ID store, tmux, and (when s.sessionRT is wired up) the session
// router's queue depth.
func (s *Server) buildSessionsData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	store := session.NewStore(cfg.HomePath)

	names := make([]string, 0, len(cfg.Sessions))
	for name := range cfg.Sessions {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := make([]sessionRow, 0, len(names))
	for _, name := range names {
		sc := cfg.Sessions[name]
		row := sessionRow{Name: name, Depth: -1}
		row.StoredID, _, _ = store.Get("session:" + name)
		row.TmuxLive = s.tmuxSessionLive(sessionTmuxTarget(name))
		if s.sessionRT != nil {
			row.Depth = s.sessionRT.SessionDepth(name)
		}
		row.Form = s.buildFormWithHarness(schema.SectionSession, &sc, cfg, "/web/config/session/"+url.PathEscape(name), name)
		row.Form.DeleteURL = "/web/session/" + url.PathEscape(name)
		rows = append(rows, row)
	}

	return sessionsPageData{Sessions: rows}, nil
}

// sessionTmuxTarget returns the tmux session name leo uses for a persistent
// session, matching internal/cli/session.go's sessionTmuxTarget.
func sessionTmuxTarget(name string) string { return "leo-session-" + name }

// tmuxSessionLive reports whether tmux has a live session for target,
// mirroring internal/cli/session.go's isTmuxSessionLive but routed through
// the lookTmux and execCommand seams so tests can stub it without shelling
// out to a real tmux binary.
func (s *Server) tmuxSessionLive(target string) bool {
	tmuxBin, err := s.lookTmux()
	if err != nil {
		return false
	}
	return s.execCommand(tmuxBin, tmux.Args("has-session", "-t", tmux.Target(target))...).Run() == nil
}

// handleSessionAdd creates a new persistent-session entry and reports back
// on the sessions page itself (HX-Refresh, not a redirect to a separate edit
// page). Unlike hosts (which round-trip an empty HostConfig{} through
// Validate() as-is), Config.Validate() requires sessions.<name>.workspace to
// be non-empty, so the new entry is seeded with cfg.DefaultWorkspace() — a
// real, working default, not an obviously-fake placeholder — so the session
// actually boots there until the operator picks a workspace of their own via
// the card's inline form.
func (s *Server) handleSessionAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	name := r.FormValue("name")
	if !validEntityName(name) {
		s.renderFlash(w, "error", entityNameError)
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if cfg.Sessions == nil {
		cfg.Sessions = make(map[string]config.SessionConfig)
	}
	if _, exists := cfg.Sessions[name]; exists {
		s.renderFlash(w, "error", fmt.Sprintf("Session %q already exists", name))
		return
	}

	cfg.Sessions[name] = config.SessionConfig{
		Workspace: cfg.DefaultWorkspace(),
	}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	// Sessions boot lazily on first use, so no restart flag: creating a
	// config entry here doesn't start anything.
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, "success", fmt.Sprintf("Session %q created — review its workspace and other settings below", name))
}

// handleSessionDelete removes a persistent-session entry and reports back
// on the sessions page (HX-Refresh, not a redirect to a separate edit page).
// Design decision, worth calling out: unlike hosts (nothing references a
// host by name), Config.Validate() already catches a dangling reference here
// — a task with `runtime: persistent` and an explicit `session: <name>`
// pointing at this entry fails ResolveSession's topology-B lookup ("task %q
// references sessions.%s which is not defined") once the entry is gone. So
// this handler does no reference scan of its own; it deletes optimistically
// and lets validateAndSave's Validate() call carry the refusal. Note this
// only covers the explicit `session:` case
// (topology B) — a task with no `session:` field gets an implicitly-named
// dedicated session (topology A) that isn't a cfg.Sessions entry at all, so
// there's nothing here for it to dangle against.
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if _, ok := cfg.Sessions[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Session %q not found", name))
		return
	}

	delete(cfg.Sessions, name)

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, "success", fmt.Sprintf("Session %q deleted", name))
}

// handleSessionReset kills a persistent session's tmux session, tells the
// session router (if wired up) to drop any queued/in-flight invocations for
// it, and clears its stored --resume session id. Mirrors `leo session
// reset` (internal/cli/session.go's newSessionResetCmd) minus the TTY
// confirmation prompt — the browser-side confirmation is confirmDelete's JS
// confirm(), wired up in the template.
func (s *Server) handleSessionReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}
	if _, ok := cfg.Sessions[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Session %q not found", name))
		return
	}

	// Notify the router first so any in-flight waiter gets a clean "reset"
	// error instead of hanging until its task timeout. Skipped silently when
	// no SessionRuntimeProvider is wired up (e.g. in tests).
	cleared := 0
	if s.sessionRT != nil {
		cleared = s.sessionRT.ResetSession(name, "web reset")
	}

	if tmuxBin, lerr := s.lookTmux(); lerr == nil {
		_ = s.execCommand(tmuxBin, tmux.Args("kill-session", "-t", tmux.Target(sessionTmuxTarget(name)))...).Run()
	}

	if err := session.NewStore(cfg.HomePath).Delete("session:" + name); err != nil {
		s.renderFlash(w, "error", "Clear stored session id: "+err.Error())
		return
	}

	s.renderFlash(w, "success", fmt.Sprintf("Session %q reset (%d queued invocation(s) dropped)", name, cleared))
}
