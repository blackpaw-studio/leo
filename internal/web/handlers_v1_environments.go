package web

import (
	"net/http"
	"sort"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// handleAPIEnvironments lists the configured named environments, names only.
// GET /api/v1/environments
//
// Order is alphabetical, not YAML order: the config map loses its source order
// at parse, and every web-UI save rewrites the file with sorted keys anyway, so
// only a sorted list is stable. Environments have no harness: each is a bare
// env map any template can run under.
func (s *Server) handleAPIEnvironments(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	names := environmentNames(cfg)
	out := make([]observe.Environment, 0, len(names))
	for _, name := range names {
		out = append(out, observe.Environment{Name: name})
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: out})
}

// handleAPITemplatesV1 lists the configured templates with their default
// environments (names only; a template's env map never crosses the API).
// GET /api/v1/templates
func (s *Server) handleAPITemplatesV1(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	out := make([]observe.Template, 0, len(cfg.Templates))
	for name, tmpl := range cfg.Templates {
		envs := tmpl.Environments
		if envs == nil {
			envs = []string{}
		}
		out = append(out, observe.Template{
			Name:         name,
			Harness:      cfg.TemplateHarness(tmpl),
			Model:        cfg.TemplateModel(tmpl),
			Environments: envs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: out})
}
