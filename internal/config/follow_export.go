package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
)

// FollowExportSchema is the export format this build reads and writes.
// Adding fields does not bump it; a follower refuses a newer one.
const FollowExportSchema = 1

// FollowExportKind tags the export document.
const FollowExportKind = "leo.follow.export"

// FollowErrorCode classifies a failed export or pull.
type FollowErrorCode string

const (
	FollowUnreachable       FollowErrorCode = "unreachable"
	FollowLeadTooOld        FollowErrorCode = "lead_too_old"
	FollowLeadIsFollower    FollowErrorCode = "lead_is_follower"
	FollowLeadConfigInvalid FollowErrorCode = "lead_config_invalid"
	FollowSelfFollow        FollowErrorCode = "self_follow"
	FollowMalformed         FollowErrorCode = "malformed"
	FollowFollowerTooOld    FollowErrorCode = "follower_too_old"
)

// FollowError is a classified follow failure. Its message starts with the
// code, so a follower can recognise the lead's refusal on ssh stderr.
type FollowError struct {
	Code   FollowErrorCode
	Detail string
}

func (e *FollowError) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// FollowExport is what `leo config export --follow` prints: the lead's
// effective templates (templates_file merged), environments, delegation and
// defaults, copied verbatim (no ~ expansion), plus identifying metadata.
// Section values keep their YAML types; a follower parses them with
// UseNumber (see ParseFollowExport) and decodes them through YAML.
type FollowExport struct {
	Schema       int                          `json:"schema"`
	Kind         string                       `json:"kind"`
	LeoVersion   string                       `json:"leo_version"`
	HomeID       string                       `json:"home_id"`
	Revision     string                       `json:"revision"`
	Templates    map[string]any               `json:"templates"`
	Environments map[string]map[string]string `json:"environments"`
	Delegation   any                          `json:"delegation"`
	Defaults     map[string]any               `json:"defaults"`
}

// BuildFollowExport builds the export of the config at path. It never
// resolves a client host: the export always describes this machine. A
// config that itself follows a lead, or that fails Validate, is refused.
func BuildFollowExport(path, leoVersion string) (FollowExport, error) {
	full, rev, err := LoadWithRevision(path)
	if err != nil {
		return FollowExport{}, err
	}
	if full.Follow != nil {
		return FollowExport{}, &FollowError{Code: FollowLeadIsFollower, Detail: fmt.Sprintf("this host follows %q; follow chains are not supported", full.Follow.Host)}
	}
	if err := full.Validate(); err != nil {
		return FollowExport{}, &FollowError{Code: FollowLeadConfigInvalid, Detail: err.Error()}
	}
	raw, err := loadUnexpanded(path)
	if err != nil {
		return FollowExport{}, err
	}
	homeID, err := HomeID(full.StatePath())
	if err != nil {
		return FollowExport{}, err
	}
	exp := FollowExport{
		Schema: FollowExportSchema, Kind: FollowExportKind, LeoVersion: leoVersion,
		HomeID: homeID, Revision: rev,
		Templates:    map[string]any{},
		Environments: raw.Environments,
	}
	for name, tmpl := range raw.Templates {
		if exp.Templates[name], err = toJSONValue(tmpl); err != nil {
			return FollowExport{}, fmt.Errorf("exporting templates.%s: %w", name, err)
		}
	}
	if raw.Delegation != nil {
		if exp.Delegation, err = toJSONValue(raw.Delegation); err != nil {
			return FollowExport{}, fmt.Errorf("exporting delegation: %w", err)
		}
	}
	defaults, err := toJSONValue(raw.Defaults)
	if err != nil {
		return FollowExport{}, fmt.Errorf("exporting defaults: %w", err)
	}
	exp.Defaults, _ = defaults.(map[string]any)
	return exp, nil
}

// ParseFollowExport parses and fully checks an export: the kind, a schema
// this build supports, and every section decoding into the config types. Any
// failure is a *FollowError (malformed or follower_too_old), so a bad pull is
// rejected whole.
func ParseFollowExport(data []byte) (FollowExport, error) {
	exp, _, err := parseFollowExport(data)
	return exp, err
}

func parseFollowExport(data []byte) (FollowExport, leadSections, error) {
	var exp FollowExport
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&exp); err != nil {
		return FollowExport{}, leadSections{}, &FollowError{Code: FollowMalformed, Detail: err.Error()}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return FollowExport{}, leadSections{}, &FollowError{Code: FollowMalformed, Detail: "trailing data after the export"}
	}
	if exp.Kind != FollowExportKind {
		return FollowExport{}, leadSections{}, &FollowError{Code: FollowMalformed, Detail: fmt.Sprintf("kind %q is not %s", exp.Kind, FollowExportKind)}
	}
	if exp.Schema > FollowExportSchema {
		return FollowExport{}, leadSections{}, &FollowError{Code: FollowFollowerTooOld, Detail: fmt.Sprintf("schema %d", exp.Schema)}
	}
	if exp.Schema < 1 {
		return FollowExport{}, leadSections{}, &FollowError{Code: FollowMalformed, Detail: fmt.Sprintf("schema %d", exp.Schema)}
	}
	sections, err := exp.decodeSections()
	if err != nil {
		return FollowExport{}, leadSections{}, err
	}
	return exp, sections, nil
}

// leadSections are an export's sections decoded into the config types,
// paths unexpanded.
type leadSections struct {
	templates    map[string]TemplateConfig
	environments map[string]map[string]string
	delegation   *DelegationConfig
	defaults     DefaultsConfig
}

func (e FollowExport) decodeSections() (leadSections, error) {
	malformed := func(section string, err error) error {
		return &FollowError{Code: FollowMalformed, Detail: fmt.Sprintf("%s: %v", section, err)}
	}
	var out leadSections
	if len(e.Templates) > 0 {
		out.templates = make(map[string]TemplateConfig, len(e.Templates))
		for name, v := range e.Templates {
			var tmpl TemplateConfig
			if err := decodeJSONValue(v, &tmpl); err != nil {
				return leadSections{}, malformed("templates."+name, err)
			}
			out.templates[name] = tmpl
		}
	}
	if e.Environments != nil {
		// A fresh copy per decode: the effective config and the ownership
		// baseline must not share maps.
		out.environments = make(map[string]map[string]string, len(e.Environments))
		for name, env := range e.Environments {
			out.environments[name] = maps.Clone(env)
		}
	}
	if e.Delegation != nil {
		out.delegation = &DelegationConfig{}
		if err := decodeJSONValue(e.Delegation, out.delegation); err != nil {
			return leadSections{}, malformed("delegation", err)
		}
	}
	if e.Defaults != nil {
		if err := decodeJSONValue(e.Defaults, &out.defaults); err != nil {
			return leadSections{}, malformed("defaults", err)
		}
	}
	return out, nil
}

// Digest is "sha256:" plus the hex digest of the canonical JSON of the four
// synced sections, independent of key order and of the metadata header, so
// a follower can tell whether a pull changed anything it uses.
func (e FollowExport) Digest() string {
	data, err := json.Marshal(struct {
		Templates    map[string]any               `json:"templates"`
		Environments map[string]map[string]string `json:"environments"`
		Delegation   any                          `json:"delegation"`
		Defaults     map[string]any               `json:"defaults"`
	}{e.Templates, e.Environments, e.Delegation, e.Defaults})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
