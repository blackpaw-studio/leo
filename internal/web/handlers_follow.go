package web

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/follow"
)

// invalidLeadLabel is the badge of a lead entry unusable on this host.
const invalidLeadLabel = "lead · invalid"

// entrySourceLabels returns an entry's badge text and machine key: set on a
// follower and, for templates, with a templates_file; both empty otherwise.
func entrySourceLabels(cfg *config.Config, kind config.EntryKind, name string) (label, key string) {
	if !cfg.IsFollower() && (kind != config.KindTemplate || !cfg.HasTemplatesFile()) {
		return "", ""
	}
	src := cfg.EntrySource(kind, name)
	return src.Label(), string(src)
}

// lockToLead makes form read-only with an "Override locally" action when the
// entry is mirrored from the lead.
func lockToLead(form *formData, cfg *config.Config, kind config.EntryKind, name, overrideURL string) {
	if !cfg.IsLeadEntry(kind, name) {
		return
	}
	form.ReadOnly, form.OverrideURL, form.Lead = true, overrideURL, cfg.FollowLead()
	form.DeleteURL = ""
}

// sortedInvalid lists the invalid lead entries of kind, by name.
func sortedInvalid(cfg *config.Config, kind config.EntryKind) []struct{ Name, Reason string } {
	entries := cfg.InvalidEntries(kind)
	out := make([]struct{ Name, Reason string }, 0, len(entries))
	for name, reason := range entries {
		out = append(out, struct{ Name, Reason string }{name, reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) handleTemplateOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.overrideAndRedirect(w, func() error {
		_, err := s.OverrideItem("", resourceByKey("templates"), name)
		return err
	}, "/config/templates/"+url.PathEscape(name))
}

func (s *Server) handleEnvironmentOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.overrideAndRedirect(w, func() error {
		_, err := s.OverrideItem("", resourceByKey("environments"), name)
		return err
	}, "/config/environments")
}

func (s *Server) handleDelegationProfileOverride(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	s.overrideAndRedirect(w, func() error {
		_, err := s.Mutate(mutateOpts{}, overrideEntry(config.KindProfile, name))
		return err
	}, "/config/delegation")
}

// overrideAndRedirect runs an override and reloads the page it came from,
// where the entry is now local and editable; a failure is flashed.
func (s *Server) overrideAndRedirect(w http.ResponseWriter, override func() error, page string) {
	if err := override(); err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	w.Header().Set("HX-Redirect", page)
	w.WriteHeader(http.StatusOK)
}

// handleFollowSync is the Settings page's Sync now: pull the lead through
// the daemon's follow loop, then refresh so the status card shows it.
func (s *Server) handleFollowSync(w http.ResponseWriter, r *http.Request) {
	allowSlowSync(w)
	if _, err := s.SyncFollow(r.Context()); err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// followCard feeds the Settings page's follow card; nil when not following.
type followCard struct {
	follow.Report
	PulledAt    string
	Age         string
	LastAttempt string
	Messages    []string
}

func buildFollowCard(cfg *config.Config, now time.Time, version string) *followCard {
	r := follow.BuildReport(cfg, now)
	if !r.Following {
		return nil
	}
	card := &followCard{Report: r, Messages: r.Messages(version)}
	if r.PulledAt != nil {
		card.PulledAt = r.PulledAt.Format(time.RFC3339)
		card.Age = fmt.Sprint(time.Duration(r.AgeSeconds) * time.Second)
	}
	if r.LastAttempt != nil {
		card.LastAttempt = r.LastAttempt.Format(time.RFC3339)
	}
	return card
}

// leoVersion is the daemon's leo version, "" when unknown.
func (c *ConfigService) leoVersion() string {
	if c.version == nil {
		return ""
	}
	return c.version()
}
