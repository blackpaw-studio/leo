package web

import (
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
)

// recentConsultLimit caps the role-routed consults listed in the delegation
// document, matching the HTML page.
const recentConsultLimit = 20

// delegationTemplateDoc, delegationRoleDoc and the types below are the JSON
// shape of GET /config/delegation.
type delegationTemplateDoc struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

type delegationRoleDoc struct {
	Name     string `json:"name"`
	UseFor   string `json:"use_for"`
	Declared bool   `json:"declared"`
}

type delegationCellDoc struct {
	Template       string `json:"template"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	InheritedModel string `json:"inherited_model"`
}

type delegationProfileDoc struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Active      bool                         `json:"active"`
	Roles       map[string]delegationCellDoc `json:"roles"`
}

// DelegationDocument is the whole delegation block as the JSON API reads it.
// An unconfigured block still answers (configured:false) so a client can show
// an empty state without probing for a 404.
type DelegationDocument struct {
	Revision                  string                  `json:"revision"`
	Configured                bool                    `json:"configured"`
	Enabled                   bool                    `json:"enabled"`
	ActiveProfile             string                  `json:"active_profile"`
	HideNativeAgents          []string                `json:"hide_native_agents"`
	HideNativeAgentsIsDefault bool                    `json:"hide_native_agents_is_default"`
	Templates                 []delegationTemplateDoc `json:"templates"`
	Roles                     []delegationRoleDoc     `json:"roles"`
	Profiles                  []delegationProfileDoc  `json:"profiles"`
	Warnings                  []string                `json:"warnings"`
	RecentConsults            []any                   `json:"recent_consults"`
}

func (c *ConfigService) buildDelegationDocument(cfg *config.Config, revision string) DelegationDocument {
	doc := DelegationDocument{
		Revision: revision, HideNativeAgents: []string{},
		Templates: []delegationTemplateDoc{}, Roles: []delegationRoleDoc{}, Profiles: []delegationProfileDoc{},
		Warnings: []string{}, RecentConsults: []any{},
	}
	d := cfg.Delegation
	if d == nil {
		return doc
	}
	doc.Configured, doc.Enabled, doc.ActiveProfile = true, d.IsEnabled(), d.ActiveProfile
	doc.HideNativeAgents = d.HiddenNativeAgents()
	if doc.HideNativeAgents == nil {
		doc.HideNativeAgents = []string{}
	}
	doc.HideNativeAgentsIsDefault = d.HideNativeAgents == nil
	for _, t := range delegationTemplates(cfg) {
		doc.Templates = append(doc.Templates, delegationTemplateDoc(t))
	}
	for _, r := range delegationRoles(d) {
		doc.Roles = append(doc.Roles, delegationRoleDoc{Name: r.Name, UseFor: r.UseFor, Declared: r.Declared})
	}
	for _, row := range delegationProfileRows(d) {
		p := d.Profiles[row.Name]
		pd := delegationProfileDoc{Name: row.Name, Description: row.Description, Active: row.Active, Roles: map[string]delegationCellDoc{}}
		for role, target := range p.Roles {
			cell := delegationCellDoc{Template: target.Template, Model: target.Model, Effort: target.Effort}
			if _, ok := cfg.Templates[target.Template]; ok {
				cell.InheritedModel, _ = cfg.RoleTargetModel(config.RoleTarget{Template: target.Template})
			}
			pd.Roles[role] = cell
		}
		doc.Profiles = append(doc.Profiles, pd)
	}
	if w := cfg.DelegationWarnings(); w != nil {
		doc.Warnings = w
	}
	if fn := c.recentConsults.Load(); fn != nil {
		for _, rec := range (*fn)() {
			if rec.Role == "" {
				continue
			}
			doc.RecentConsults = append(doc.RecentConsults, rec)
			if len(doc.RecentConsults) == recentConsultLimit {
				break
			}
		}
	}
	return doc
}

func (c *ConfigService) readDelegation() (DelegationDocument, error) {
	cfg, rev, err := c.loadLocked()
	if err != nil {
		return DelegationDocument{}, err
	}
	return c.buildDelegationDocument(cfg, rev), nil
}

// delegationWrite runs mut as one revision-gated write and answers with the
// refreshed delegation document as the item.
func (c *ConfigService) delegationWrite(w http.ResponseWriter, r *http.Request, status int, scope validationScope, mut delegationMutator) {
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	scope.Resource = "delegation"
	result, err := c.Mutate(mutateOpts{IfMatch: ifMatch, Scope: scope}, mut)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	out := writeResponse{Revision: result.Revision, RestartRequired: result.Restart, Warning: result.Warning}
	if doc, err := c.readDelegation(); err == nil {
		out.Item = doc
	}
	w.Header().Set("ETag", `"`+result.Revision+`"`)
	writeConfigData(w, status, out)
}

// delegationBody decodes a request body, writing the 400 itself on failure.
func delegationBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var body T
	if err := decodeConfigBody(w, r, &body); err != nil {
		writeConfigError(w, err)
		return body, false
	}
	return body, true
}

func (c *ConfigService) registerDelegationRoutes(route func(string, http.HandlerFunc)) {
	route("GET /config/delegation", c.handleDelegationGet)
	route("GET /config/delegation/preview", c.handleDelegationPreviewAPI)
	route("PUT /config/delegation/profiles/{profile}/roles/{role}", c.handleDelegationCellAPI)
	route("PUT /config/delegation/roles/{role}", c.handleDelegationUseForAPI)
	route("PUT /config/delegation/active", c.handleDelegationActiveAPI)
	route("PUT /config/delegation/enabled", c.handleDelegationEnabledAPI)
	route("PUT /config/delegation/hide-agents", c.handleDelegationHideAPI)
	route("POST /config/delegation/roles", c.handleDelegationRoleAddAPI)
	route("POST /config/delegation/roles/{role}/rename", c.handleDelegationRoleRenameAPI)
	route("DELETE /config/delegation/roles/{role}", c.handleDelegationRoleDeleteAPI)
	route("POST /config/delegation/profiles", c.handleDelegationProfileAddAPI)
	route("POST /config/delegation/profiles/{profile}/rename", c.handleDelegationProfileRenameAPI)
	route("POST /config/delegation/profiles/{profile}/duplicate", c.handleDelegationProfileDuplicateAPI)
	route("DELETE /config/delegation/profiles/{profile}", c.handleDelegationProfileDeleteAPI)
}

func (c *ConfigService) handleDelegationGet(w http.ResponseWriter, _ *http.Request) {
	doc, err := c.readDelegation()
	if err != nil {
		writeConfigError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+doc.Revision+`"`)
	writeConfigData(w, http.StatusOK, doc)
}

type previewChangeDoc struct {
	Role   string   `json:"role"`
	Kind   string   `json:"kind"`
	Before cellBody `json:"before"`
	After  cellBody `json:"after"`
}

type previewDoc struct {
	Profile string             `json:"profile"`
	Changes []previewChangeDoc `json:"changes"`
	Missing []string           `json:"missing"`
}

func (c *ConfigService) handleDelegationPreviewAPI(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := c.loadLocked()
	if err != nil {
		writeConfigError(w, err)
		return
	}
	data, err := previewDelegation(cfg, r.URL.Query().Get("profile"))
	if err != nil {
		writeConfigError(w, err)
		return
	}
	doc := previewDoc{Profile: data.Profile, Changes: []previewChangeDoc{}, Missing: data.Missing}
	for _, ch := range data.Changes {
		doc.Changes = append(doc.Changes, previewChangeDoc{Role: ch.Role, Kind: ch.Kind, Before: cellOf(ch.Before), After: cellOf(ch.After)})
	}
	writeConfigData(w, http.StatusOK, doc)
}

type cellBody struct {
	Template string `json:"template"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}

func cellOf(t config.RoleTarget) cellBody {
	return cellBody{Template: t.Template, Model: t.Model, Effort: t.Effort}
}

func (c *ConfigService) handleDelegationCellAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[cellBody](w, r)
	if !ok {
		return
	}
	profile, role := r.PathValue("profile"), r.PathValue("role")
	c.delegationWrite(w, r, http.StatusOK, validationScope{Profile: profile, Role: role},
		setCell(profile, role, config.RoleTarget{Template: body.Template, Model: body.Model, Effort: body.Effort}))
}

func (c *ConfigService) handleDelegationUseForAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[struct {
		UseFor string `json:"use_for"`
	}](w, r)
	if !ok {
		return
	}
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, setUseFor(r.PathValue("role"), body.UseFor))
}

func (c *ConfigService) handleDelegationActiveAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[struct {
		Profile string `json:"profile"`
	}](w, r)
	if !ok {
		return
	}
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, setActiveProfile(body.Profile))
}

func (c *ConfigService) handleDelegationEnabledAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[struct {
		Enabled *bool `json:"enabled"`
	}](w, r)
	if !ok {
		return
	}
	if body.Enabled == nil {
		writeConfigError(w, errBadRequest(`body must be {"enabled": true|false}`))
		return
	}
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, setEnabled(*body.Enabled))
}

func (c *ConfigService) handleDelegationHideAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[struct {
		Names *[]string `json:"names"`
	}](w, r)
	if !ok {
		return
	}
	if body.Names == nil {
		writeConfigError(w, errBadRequest(`body must be {"names": [...]}`))
		return
	}
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, setHiddenAgents(*body.Names))
}

func (c *ConfigService) handleDelegationRoleAddAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[struct {
		Name     string `json:"name"`
		Template string `json:"template"`
	}](w, r)
	if !ok {
		return
	}
	c.delegationWrite(w, r, http.StatusCreated, validationScope{}, addRole(body.Name, body.Template))
}

// newNameBody is the body of rename and duplicate.
type newNameBody struct {
	NewName string `json:"new_name"`
}

func (c *ConfigService) handleDelegationRoleRenameAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[newNameBody](w, r)
	if !ok {
		return
	}
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, renameRole(r.PathValue("role"), body.NewName))
}

func (c *ConfigService) handleDelegationRoleDeleteAPI(w http.ResponseWriter, r *http.Request) {
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, deleteRole(r.PathValue("role")))
}

func (c *ConfigService) handleDelegationProfileAddAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[struct {
		Name string `json:"name"`
	}](w, r)
	if !ok {
		return
	}
	c.delegationWrite(w, r, http.StatusCreated, validationScope{}, addProfile(body.Name))
}

func (c *ConfigService) handleDelegationProfileRenameAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[newNameBody](w, r)
	if !ok {
		return
	}
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, renameProfile(r.PathValue("profile"), body.NewName))
}

func (c *ConfigService) handleDelegationProfileDuplicateAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := delegationBody[newNameBody](w, r)
	if !ok {
		return
	}
	c.delegationWrite(w, r, http.StatusCreated, validationScope{}, duplicateProfile(r.PathValue("profile"), body.NewName))
}

func (c *ConfigService) handleDelegationProfileDeleteAPI(w http.ResponseWriter, r *http.Request) {
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, deleteProfile(r.PathValue("profile")))
}
