package web

import (
	"errors"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
)

// CodeOwnedByLead refuses a write to an entry a follower mirrors from its
// lead (409, data {kind, name, lead}).
const CodeOwnedByLead = "owned_by_lead"

// ownedByLead maps a *config.OwnedByLeadError to its 409, nil otherwise.
func ownedByLead(err error) *ConfigError {
	var owned *config.OwnedByLeadError
	if !errors.As(err, &owned) {
		return nil
	}
	return &ConfigError{Status: http.StatusConflict, Code: CodeOwnedByLead, Message: owned.Error(),
		Data: map[string]string{"kind": string(owned.Kind), "name": owned.Name, "lead": owned.Lead}}
}

// followKindOf is the follow entry kind a config resource holds.
func followKindOf(res *configResource) (config.EntryKind, bool) {
	switch res.Key {
	case "templates":
		return config.KindTemplate, true
	case "environments":
		return config.KindEnvironment, true
	}
	return "", false
}

// itemSource is where a follower's entry comes from; "" when not following,
// so documents without a lead stay unchanged.
func itemSource(cfg *config.Config, kind config.EntryKind, name string) string {
	if !cfg.IsFollower() {
		return ""
	}
	return string(cfg.EntrySource(kind, name))
}

// invalidItemDocument describes a lead entry that is invalid on this host:
// no values, only its source and why.
func invalidItemDocument(name, reason string) itemDocument {
	return itemDocument{Name: name, Values: map[string]any{}, Source: string(config.SourceLead), Invalid: reason}
}

// invalidItemDocuments lists res's invalid lead entries.
func invalidItemDocuments(cfg *config.Config, res *configResource) []itemDocument {
	kind, ok := followKindOf(res)
	if !ok {
		return nil
	}
	var docs []itemDocument
	for name, reason := range cfg.InvalidEntries(kind) {
		docs = append(docs, invalidItemDocument(name, reason))
	}
	return docs
}

// overrideEntry is the apply func of an override: the lead entry becomes a
// local one with the same content, which the save writes to leo.yaml.
func overrideEntry(kind config.EntryKind, name string) func(*config.Config) error {
	return func(cfg *config.Config) error {
		err := cfg.OverrideLocally(kind, name)
		var unknown *config.UnknownEntryError
		if errors.As(err, &unknown) {
			return errNotFound("%s", err.Error())
		}
		return err
	}
}

// OverrideItem copies the lead's template or environment into leo.yaml as a
// local override.
func (c *ConfigService) OverrideItem(ifMatch string, res *configResource, name string) (WriteResult, error) {
	kind, ok := followKindOf(res)
	if !ok {
		return WriteResult{}, errBadRequest("%s cannot be overridden", res.Label)
	}
	return c.Mutate(mutateOpts{IfMatch: ifMatch, Item: c.itemOf(res, name)}, overrideEntry(kind, name))
}

func (c *ConfigService) registerOverrideRoutes(route func(string, http.HandlerFunc)) {
	route("POST /config/{resource}/{name}/override", c.handleConfigOverride)
	route("POST /config/delegation/profiles/{profile}/override", c.handleDelegationProfileOverrideAPI)
}

func (c *ConfigService) handleConfigOverride(w http.ResponseWriter, r *http.Request) {
	res, name, err := resourceOf(r, true)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	result, err := c.OverrideItem(ifMatch, res, name)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	c.respondWrite(w, http.StatusOK, result)
}

func (c *ConfigService) handleDelegationProfileOverrideAPI(w http.ResponseWriter, r *http.Request) {
	c.delegationWrite(w, r, http.StatusOK, validationScope{}, overrideEntry(config.KindProfile, r.PathValue("profile")))
}
