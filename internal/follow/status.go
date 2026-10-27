package follow

import (
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Report is a follower's sync state, shared by `leo sync --status`, the
// config API (GET /config/follow), /api/v1/follow and the Settings page.
type Report struct {
	Following bool   `json:"following"`
	Host      string `json:"host,omitempty"`
	SSH       string `json:"ssh,omitempty"`
	Interval  string `json:"interval,omitempty"`
	// PulledAt is the last successful pull; nil before the first.
	PulledAt     *time.Time `json:"pulled_at,omitempty"`
	AgeSeconds   int64      `json:"age_seconds,omitempty"`
	Stale        bool       `json:"stale"`
	LeadRevision string     `json:"lead_revision,omitempty"`
	LeadVersion  string     `json:"lead_version,omitempty"`
	LastAttempt  *time.Time `json:"last_attempt,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	// LastErrorCode is a config.FollowErrorCode, empty when the last attempt
	// succeeded.
	LastErrorCode string   `json:"last_error_code,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	// Invalid maps an entry kind (template, environment, profile, or
	// "defaults" for dropped defaults keys) to name → reason.
	Invalid map[string]map[string]string `json:"invalid,omitempty"`
}

// BuildReport describes cfg's follow state at now.
func BuildReport(cfg *config.Config, now time.Time) Report {
	if !cfg.IsFollower() {
		return Report{}
	}
	r := Report{
		Following: true,
		Host:      cfg.Follow.Host,
		SSH:       cfg.Client.Hosts[cfg.Follow.Host].SSH,
		Interval:  cfg.FollowInterval().String(),
		Warnings:  cfg.FollowWarnings(),
	}
	if fc := cfg.FollowCacheInfo(); fc != nil {
		pulled := fc.PulledAt
		r.PulledAt = &pulled
		r.AgeSeconds = int64(now.Sub(pulled) / time.Second)
		r.Stale = fc.Stale(now, cfg.FollowInterval())
		r.LeadRevision, r.LeadVersion = fc.LeadRevision, fc.LeadVersion
	}
	if st, err := config.ReadFollowStatus(cfg.StatePath()); err == nil && !st.LastAttempt.IsZero() {
		attempt := st.LastAttempt
		r.LastAttempt = &attempt
		r.LastError, r.LastErrorCode = st.LastError, string(st.LastErrorCode)
	}
	add := func(kind string, entries map[string]string) {
		if len(entries) == 0 {
			return
		}
		if r.Invalid == nil {
			r.Invalid = map[string]map[string]string{}
		}
		r.Invalid[kind] = entries
	}
	for _, kind := range []config.EntryKind{config.KindTemplate, config.KindEnvironment, config.KindProfile} {
		add(string(kind), cfg.InvalidEntries(kind))
	}
	add("defaults", cfg.InvalidLeadDefaults())
	return r
}
