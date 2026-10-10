package web

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// environmentCard is one entry of the Environments page: a name paired with
// the schema-driven form for its env map.
type environmentCard struct {
	Name string
	Form formData
}

// environmentsPageData feeds page_config_environments.
type environmentsPageData struct {
	Environments []environmentCard
}

// buildEnvironmentsData assembles the name-sorted environment cards.
func (s *Server) buildEnvironmentsData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	names := make([]string, 0, len(cfg.Environments))
	for name := range cfg.Environments {
		names = append(names, name)
	}
	sort.Strings(names)

	cards := make([]environmentCard, 0, len(names))
	for _, name := range names {
		entry := schema.EnvironmentEntry{Env: cfg.Environments[name]}
		form := s.buildForm(schema.SectionEnvironment, &entry, cfg, "/web/config/environment/"+url.PathEscape(name))
		form.DeleteURL = "/web/environment/" + url.PathEscape(name)
		cards = append(cards, environmentCard{Name: name, Form: form})
	}
	return environmentsPageData{Environments: cards}, nil
}

// handleConfigEnvironmentSave saves one environment's env map. Running agents
// keep their launch env until restarted (the same as any config change), so it
// raises the agents-restart-needed banner.
func (s *Server) handleConfigEnvironmentSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionEnvironment,
		func(cfg *config.Config) (any, bool) {
			env, ok := cfg.Environments[name]
			return &schema.EnvironmentEntry{Env: env}, ok
		},
		func(cfg *config.Config, v any) { cfg.Environments[name] = v.(*schema.EnvironmentEntry).Env },
		nil,
		fmt.Sprintf("Environment %q saved", name), &s.agentsRestartNeeded)
}

// handleEnvironmentAdd creates an empty named environment.
func (s *Server) handleEnvironmentAdd(w http.ResponseWriter, r *http.Request) {
	defer s.lockConfigWrite()()
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
	if _, exists := cfg.Environments[name]; exists {
		s.renderFlash(w, "error", fmt.Sprintf("Environment %q already exists", name))
		return
	}
	if cfg.Environments == nil {
		cfg.Environments = make(map[string]map[string]string)
	}
	cfg.Environments[name] = map[string]string{}
	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, "success", fmt.Sprintf("Environment %q created — add its variables below", name))
}

// handleEnvironmentDelete removes an environment. Validation rejects the save
// while defaults, a template or a task still names it, so the flash lists the
// reference instead of leaving a dangling name behind.
func (s *Server) handleEnvironmentDelete(w http.ResponseWriter, r *http.Request) {
	defer s.lockConfigWrite()()
	name := r.PathValue("name")
	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}
	if _, ok := cfg.Environments[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Environment %q not found", name))
		return
	}
	delete(cfg.Environments, name)
	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, "success", fmt.Sprintf("Environment %q deleted", name))
}
