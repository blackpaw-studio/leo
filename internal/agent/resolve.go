package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// ErrAmbiguous is returned by Manager.Resolve when a query matches more than
// one live agent at the same tier.
type ErrAmbiguous struct {
	Query   string
	Matches []string
}

func (e *ErrAmbiguous) Error() string {
	return fmt.Sprintf("ambiguous agent query %q — matches %v; use the full name to disambiguate", e.Query, e.Matches)
}

// ErrNotFound is returned by Manager.Resolve when a query matches no live
// agent.
type ErrNotFound struct {
	Query string
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("no agent matches %q", e.Query)
}

// Resolve looks up a running agent by a flexible identifier.
//
// Matching tiers (first non-empty tier wins):
//  1. Exact full name (case-insensitive).
//  2. Exact stored Repo (e.g. "owner/name").
//  3. Repo short — the segment after "/" for "owner/name" repos, or the full
//     value for slashless repos.
//  4. Suffix "-<query>" on the full name.
//
// Only live agents participate; a stopped agent is never returned.
func (m *Manager) Resolve(query string) (Record, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Record{}, fmt.Errorf("empty agent query")
	}

	live := m.sup.EphemeralAgents()
	if len(live) == 0 {
		return Record{}, &ErrNotFound{Query: query}
	}

	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}
	// A missing agentstore file means no agents have been persisted — treat it
	// as an empty store so tier 2/3 silently fall through. Any other error
	// (JSON parse, permission denied) is surfaced so the user gets a real
	// diagnostic instead of an opaque ErrNotFound.
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Record{}, fmt.Errorf("loading agentstore: %w", err)
	}

	type row struct {
		name string
		rec  agentstore.Record
	}
	rows := make([]row, 0, len(live))
	for name := range live {
		rows = append(rows, row{name: name, rec: stored[name]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	q := strings.ToLower(query)
	suffixProbe := "-" + q

	var exactName, exactRepo, repoShort, suffix []row
	for _, r := range rows {
		if strings.EqualFold(r.name, query) {
			exactName = append(exactName, r)
		}
		if r.rec.Repo != "" && strings.EqualFold(r.rec.Repo, query) {
			exactRepo = append(exactRepo, r)
		}
		if short := ShortRepo(r.rec.Repo); short != "" && strings.EqualFold(short, query) {
			repoShort = append(repoShort, r)
		}
		if strings.HasSuffix(strings.ToLower(r.name), suffixProbe) {
			suffix = append(suffix, r)
		}
	}

	for _, tier := range [][]row{exactName, exactRepo, repoShort, suffix} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return m.hydrate(tier[0].name, live[tier[0].name], stored), nil
		default:
			names := make([]string, 0, len(tier))
			for _, r := range tier {
				names = append(names, r.name)
			}
			return Record{}, &ErrAmbiguous{Query: query, Matches: names}
		}
	}
	return Record{}, &ErrNotFound{Query: query}
}

// ShortRepo returns the repo-short segment of a stored Repo value.
// For "owner/name" it returns "name"; for a slashless value it returns the
// value itself. For an empty value it returns "".
func ShortRepo(repo string) string {
	if repo == "" {
		return ""
	}
	if idx := strings.Index(repo, "/"); idx >= 0 {
		return repo[idx+1:]
	}
	return repo
}

// ValidateRepo enforces the shape the resolver and short-name derivation
// assume: a non-empty, whitespace-free string that is either a bare name or
// exactly one owner/repo slash with both segments non-empty. Rejecting
// malformed input at spawn time keeps ShortRepo deterministic and prevents
// agentstore records that the resolver cannot reason about.
func ValidateRepo(repo string) error {
	if repo == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(repo) != repo {
		return fmt.Errorf("repo %q has leading/trailing whitespace", repo)
	}
	if strings.ContainsAny(repo, " \t\r\n") {
		return fmt.Errorf("repo %q contains whitespace", repo)
	}
	if strings.Count(repo, "/") > 1 {
		return fmt.Errorf("repo %q must be owner/repo or bare name, got multiple slashes", repo)
	}
	if idx := strings.Index(repo, "/"); idx >= 0 {
		owner, name := repo[:idx], repo[idx+1:]
		if owner == "" || name == "" {
			return fmt.Errorf("repo %q has empty owner or name segment", repo)
		}
	}
	return nil
}

func (m *Manager) hydrate(name string, state ProcessState, stored map[string]agentstore.Record) Record {
	r := Record{
		Name:      name,
		Status:    state.Status,
		StartedAt: state.StartedAt,
		Restarts:  state.Restarts,
	}
	mergeStored(&r, stored)
	return r
}

// mergeStored copies persisted metadata (Template, Repo, Workspace, Env) onto
// a Record identified by r.Name. Live state fields (Status, StartedAt,
// Restarts) are left untouched. Shared between Manager.List and Manager.hydrate
// so adding a new persisted field only needs one edit.
func mergeStored(r *Record, stored map[string]agentstore.Record) {
	s, ok := stored[r.Name]
	if !ok {
		return
	}
	r.Template = s.Template
	r.Repo = s.Repo
	r.Workspace = s.Workspace
	r.Env = s.Env
}
