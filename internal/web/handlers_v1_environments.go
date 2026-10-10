package web

import (
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
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
	cfg, ok := s.loadConfigForAPI(w)
	if !ok {
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
	cfg, ok := s.loadConfigForAPI(w)
	if !ok {
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

// errConfigUnavailable is what an API client is told when the config will not
// load. The loader's own error can quote the rejected file's contents (a YAML
// type error echoes the offending value), and /api/* is reachable with the
// agent token, so the detail goes to the daemon log only.
var errConfigUnavailable = errors.New("config unavailable")

// loadConfigForAPI loads the config for an /api handler. On failure it logs the
// detail, answers a sanitized 500 and reports false.
func (s *Server) loadConfigForAPI(w http.ResponseWriter) (*config.Config, bool) {
	cfg, err := s.loadConfig()
	if err != nil {
		log.Printf("web: loading config for an API request: %v", err)
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: errConfigUnavailable.Error(), Code: codeConfigUnavailable})
		return nil, false
	}
	return cfg, true
}
