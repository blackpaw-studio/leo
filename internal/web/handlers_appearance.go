package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// Agent appearance routes (docs/configuration/appearance.md). The rules live
// in leo.yaml and go through the config API's write path; an agent's manual
// values live on its agentstore record.

// Error classes of the appearance routes.
const (
	CodeInvalidAppearanceRule = "invalid_appearance_rule"
	CodeInvalidAppearance     = "invalid_appearance"
	CodeAmbiguous             = "ambiguous"
)

// maxAppearanceBody caps PUT /agents/{name}/appearance: two short strings.
const maxAppearanceBody = 4 << 10

// appearanceRulesDocument is the body of GET and PUT /appearance/rules.
type appearanceRulesDocument struct {
	Revision string                       `json:"revision"`
	Rules    config.AppearanceRuleList    `json:"rules"`
	Errors   []config.AppearanceRuleError `json:"errors"`
}

func newAppearanceRulesDocument(rules config.AppearanceRuleList, revision string) appearanceRulesDocument {
	if rules == nil {
		rules = config.AppearanceRuleList{}
	}
	return appearanceRulesDocument{Revision: revision, Rules: rules, Errors: rules.Errors()}
}

// handleAppearanceRulesGet lists the rules in order, with the revision to
// send back and the problems of any rule hand-edited invalid on disk.
// GET /appearance/rules
func (c *ConfigService) handleAppearanceRulesGet(w http.ResponseWriter, _ *http.Request) {
	cfg, rev, err := config.LoadWithRevision(c.configPath)
	if err != nil {
		writeConfigError(w, configUnavailable(err))
		return
	}
	w.Header().Set("ETag", `"`+rev+`"`)
	writeConfigData(w, http.StatusOK, newAppearanceRulesDocument(cfg.AppearanceRules, rev))
}

type appearanceRulesBody struct {
	Rules *config.AppearanceRuleList `json:"rules"`
}

// handleAppearanceRulesPut replaces the whole list atomically under the
// config write lock. If-Match is required (428 without it, 409 when stale);
// the first invalid rule is a 400 carrying its index and reason. The save
// reloads the daemon, which republishes every agent's appearance.
// PUT /appearance/rules  {"rules": [...]}
func (c *ConfigService) handleAppearanceRulesPut(w http.ResponseWriter, r *http.Request) {
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	var body appearanceRulesBody
	if err := decodeConfigBody(w, r, &body); err != nil {
		writeConfigError(w, err)
		return
	}
	if body.Rules == nil {
		writeConfigError(w, errBadRequest(`body must be {"rules": [...]}`))
		return
	}
	rules := *body.Rules
	if errs := rules.Errors(); len(errs) > 0 {
		writeConfigError(w, errInvalidAppearanceRule(errs[0]))
		return
	}
	res, err := c.Mutate(mutateOpts{
		IfMatch: ifMatch,
		Item: func(cfg *config.Config, revision string) any {
			return newAppearanceRulesDocument(cfg.AppearanceRules, revision)
		},
	}, func(cfg *config.Config) error {
		cfg.AppearanceRules = rules
		return nil
	})
	if err != nil {
		writeConfigError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+res.Revision+`"`)
	writeConfigData(w, http.StatusOK, res.Item)
}

func errInvalidAppearanceRule(e config.AppearanceRuleError) *ConfigError {
	return &ConfigError{Status: http.StatusBadRequest, Code: CodeInvalidAppearanceRule,
		Message: fmt.Sprintf("appearance_rules[%d]: %s", e.Index, e.Reason), Data: e}
}

// AppearanceService is the agent core behind PUT /agents/{name}/appearance;
// agent.Manager implements it.
type AppearanceService interface {
	Resolve(query string) (agent.Record, error)
	SetAppearance(name string, patch agentstore.AppearancePatch) (*observe.Appearance, error)
}

// agentAppearanceResponse is the data of a successful appearance PUT, shaped
// like agent_appearance_changed: appearance is null when nothing is set.
type agentAppearanceResponse struct {
	Agent      string              `json:"agent"`
	Appearance *observe.Appearance `json:"appearance"`
}

// AgentAppearanceHandler serves PUT /agents/{name}/appearance for the socket
// and /api/v1 alike, so both answer with the same bytes. The body is
// {emoji?, color?}: a string sets, null clears, an absent key is unchanged.
// svc returns nil when no agent service is attached (503).
func AgentAppearanceHandler(svc func() AppearanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := svc()
		if s == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent service not available"})
			return
		}
		patch, err := decodeAppearancePatch(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error(), Code: CodeBadRequest})
			return
		}
		rec, status, err := resolveAgentQuery(s, r.PathValue("name"))
		if err != nil {
			writeJSON(w, status, apiResponse{Error: err.Error(), Code: resolveErrorCode(status)})
			return
		}
		resolved, err := s.SetAppearance(rec.Name, patch)
		if err != nil {
			writeAppearanceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: agentAppearanceResponse{Agent: rec.Name, Appearance: resolved}})
	}
}

// resolveErrorCode classifies resolveAgentQuery's status.
func resolveErrorCode(status int) string {
	switch status {
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeAmbiguous
	}
	return ""
}

func writeAppearanceError(w http.ResponseWriter, err error) {
	var invalid *agent.InvalidAppearanceError
	var nf *agent.ErrNotFound
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: invalid.Reason, Code: CodeInvalidAppearance,
			Data: map[string]string{"field": invalid.Field, "reason": invalid.Reason}})
	case errors.As(err, &nf):
		writeJSON(w, http.StatusNotFound, apiResponse{Error: err.Error(), Code: CodeNotFound})
	default:
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
	}
}

// decodeAppearancePatch reads {emoji?, color?}, telling an absent key
// (unchanged) from null (clear). Unknown keys and non-string values are
// refused.
func decodeAppearancePatch(w http.ResponseWriter, r *http.Request) (agentstore.AppearancePatch, error) {
	var body struct {
		Emoji json.RawMessage `json:"emoji"`
		Color json.RawMessage `json:"color"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAppearanceBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return agentstore.AppearancePatch{}, errors.New("request body is required")
		}
		return agentstore.AppearancePatch{}, fmt.Errorf("malformed JSON body: %v", err)
	}
	emoji, err := appearanceField("emoji", body.Emoji)
	if err != nil {
		return agentstore.AppearancePatch{}, err
	}
	color, err := appearanceField("color", body.Color)
	if err != nil {
		return agentstore.AppearancePatch{}, err
	}
	return agentstore.AppearancePatch{Emoji: emoji, Color: color}, nil
}

// appearanceField maps one key of the body: absent is nil (unchanged), null
// is "" (clear), a string is itself.
func appearanceField(name string, raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	if string(raw) == "null" {
		cleared := ""
		return &cleared, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s must be a string or null", name)
	}
	return &s, nil
}

// appearanceService is the agent service as an AppearanceService, nil when
// none is attached or it cannot set appearance.
func (s *Server) appearanceService() AppearanceService {
	svc, ok := s.agentSvc.(AppearanceService)
	if !ok {
		return nil
	}
	return svc
}
