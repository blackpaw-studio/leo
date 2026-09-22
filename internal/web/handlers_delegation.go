package web

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
)

type delegationPageData struct {
	Config    *config.DelegationConfig
	Templates []string
	Warnings  []string
	Records   []any
}

func (s *Server) buildDelegationData(_ *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	d := cfg.Delegation
	if d == nil {
		return delegationPageData{}, nil
	}
	templates := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		templates = append(templates, name)
	}
	sort.Strings(templates)
	var records []any
	for _, rec := range s.consults.Records() {
		if rec.Role != "" {
			records = append(records, rec)
			if len(records) == 20 {
				break
			}
		}
	}
	return delegationPageData{Config: d, Templates: templates, Warnings: cfg.DelegationWarnings(), Records: records}, nil
}

func (s *Server) delegationMutation(w http.ResponseWriter, r *http.Request, apply func(*config.Config) error) {
	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	if err := apply(cfg); err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	if msg := s.validateAndSave(cfg); msg != "" {
		s.renderFlash(w, "error", msg)
		return
	}
	warn := s.reloadConfigOrWarn()
	typ, msg := appendReloadWarning("success", "Delegation saved", warn)
	s.renderFlash(w, typ, msg)
}

func (s *Server) handleDelegationCell(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		if cfg.Delegation == nil {
			return fmt.Errorf("delegation not configured")
		}
		profile, role := r.FormValue("profile"), r.FormValue("role")
		p, ok := cfg.Delegation.Profiles[profile]
		if !ok {
			return fmt.Errorf("profile %q not found", profile)
		}
		template := r.FormValue("template")
		if template == "" {
			delete(p.Roles, role)
		} else {
			p.Roles[role] = config.RoleTarget{Template: template, Model: r.FormValue("model"), Effort: r.FormValue("effort")}
		}
		cfg.Delegation.Profiles[profile] = p
		return nil
	})
}

func (s *Server) handleDelegationUseFor(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		if cfg.Delegation == nil {
			return fmt.Errorf("delegation not configured")
		}
		role := r.FormValue("role")
		spec, ok := cfg.Delegation.Roles[role]
		if !ok {
			return fmt.Errorf("role %q not found", role)
		}
		spec.UseFor = r.FormValue("use_for")
		cfg.Delegation.Roles[role] = spec
		return nil
	})
}

func (s *Server) handleDelegationActive(w http.ResponseWriter, r *http.Request) {
	s.delegationMutation(w, r, func(cfg *config.Config) error {
		if cfg.Delegation == nil {
			return fmt.Errorf("delegation not configured")
		}
		name := r.FormValue("profile")
		if _, ok := cfg.Delegation.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		cfg.Delegation.ActiveProfile = name
		return nil
	})
}
