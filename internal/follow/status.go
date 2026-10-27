package follow

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/update"
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

// Messages lists the report's problems as one-line warnings for `leo
// validate` and service start: why the lead layer is missing, a stale cache,
// the last pull's error, a lead running a newer leo than localVersion, and
// every lead entry ignored on this host.
func (r Report) Messages(localVersion string) []string {
	if !r.Following {
		return nil
	}
	msgs := slices.Clone(r.Warnings)
	if r.Stale && r.PulledAt != nil {
		age := time.Duration(r.AgeSeconds) * time.Second
		msgs = append(msgs, fmt.Sprintf("the cache of lead %q is stale: last pulled %s ago (interval %s)", r.Host, age, r.Interval))
	}
	if r.LastError != "" {
		msgs = append(msgs, fmt.Sprintf("the last pull from %q failed: %s", r.Host, r.LastError))
	}
	if leadIsNewer(localVersion, r.LeadVersion) {
		msgs = append(msgs, fmt.Sprintf("lead %q runs leo %s, newer than this host's %s: settings only it knows are ignored here", r.Host, r.LeadVersion, localVersion))
	}
	for _, key := range slices.Sorted(maps.Keys(r.Invalid["defaults"])) {
		msgs = append(msgs, fmt.Sprintf("defaults key %s from lead %q is ignored on this host: %s", key, r.Host, r.Invalid["defaults"][key]))
	}
	for _, kind := range []config.EntryKind{config.KindTemplate, config.KindEnvironment, config.KindProfile} {
		entries := r.Invalid[string(kind)]
		for _, name := range slices.Sorted(maps.Keys(entries)) {
			msgs = append(msgs, fmt.Sprintf("%s %q from lead %q is invalid on this host: %s", kind, name, r.Host, entries[name]))
		}
	}
	return msgs
}

// leadIsNewer reports whether lead is a newer release than local; a dev or
// unknown build on either side never warns.
func leadIsNewer(local, lead string) bool {
	if local == "" || local == "dev" || lead == "" || lead == "dev" {
		return false
	}
	return update.IsNewer(local, lead)
}
