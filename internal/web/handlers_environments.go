package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"sort"

	"github.com/blackpaw-studio/leo/internal/agentstore"
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

// handleEnvironmentRename re-keys an environment and rewrites every reference
// to it (defaults, templates, tasks via config.RenameEnvironment) plus the
// per-agent override lists persisted in the agentstore, which resolve by name
// on the next restart. The name never reaches an agent process (only the merged
// values do), and those values are unchanged, so no restart is needed and the
// agents-restart banner stays down.
//
// The rename form targets #flash-container (as the template rename form does),
// so every outcome is a flash; success also refreshes the page to show the
// renamed card.
func (s *Server) handleEnvironmentRename(w http.ResponseWriter, r *http.Request) {
	defer s.lockConfigWrite()()
	name := r.PathValue("name")

	newName := r.FormValue("new_name")
	if newName == "" {
		s.renderFlashToContainer(w, "error", "New name is required")
		return
	}
	if !validEntityName(newName) {
		s.renderFlashToContainer(w, "error", entityNameError)
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlashToContainer(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}
	if _, ok := cfg.Environments[name]; !ok {
		s.renderFlashToContainer(w, "error", fmt.Sprintf("Environment %q not found", name))
		return
	}
	renamed, err := config.RenameEnvironment(cfg, name, newName)
	if err != nil {
		s.renderFlashToContainer(w, "error", err.Error())
		return
	}
	if errMsg := s.validateAndSave(renamed); errMsg != "" {
		s.renderFlashToContainer(w, "error", errMsg)
		return
	}

	renameInAgentRecords(renamed.HomePath, name, newName)
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Refresh", "true")
	s.renderFlashToContainer(w, "success", fmt.Sprintf("Environment %q renamed to %q", name, newName))
}

// renameInAgentRecords moves oldName to newName in every persisted agent
// record's environment override list. Best-effort: a failure must not fail a
// rename that already saved config, so it is logged and the loop continues.
func renameInAgentRecords(homePath, oldName, newName string) {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		// #nosec G706 -- names are validated identifiers (validEntityName / existing config keys); no control chars can reach the log
		log.Printf("environment rename %q→%q: loading agentstore failed: %v", oldName, newName, err)
		return
	}
	for recName, rec := range records {
		if !slices.Contains(rec.Environments, oldName) {
			continue
		}
		if err := agentstore.Update(homePath, recName, func(r agentstore.Record) agentstore.Record {
			r.Environments = slices.Clone(r.Environments)
			for i, e := range r.Environments {
				if e == oldName {
					r.Environments[i] = newName
				}
			}
			return r
		}); err != nil {
			// #nosec G706 -- names are validated identifiers (validEntityName / existing config + agentstore keys); no control chars can reach the log
			log.Printf("environment rename %q→%q: agentstore.Update(%q) failed: %v", oldName, newName, recName, err)
		}
	}
}
