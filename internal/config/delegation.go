package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"gopkg.in/yaml.v3"
)

// ErrDelegationDisabled indicates that config has no delegation block.
var ErrDelegationDisabled = errors.New("delegation is not configured")

var delegationNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// DelegationConfig maps stable work roles to templates by profile.
type DelegationConfig struct {
	Roles         map[string]RoleSpec `yaml:"roles,omitempty"`
	ActiveProfile string              `yaml:"active_profile"`
	Profiles      map[string]Profile  `yaml:"profiles"`
}

// RoleSpec describes the profile-independent purpose of a role.
type RoleSpec struct {
	UseFor string `yaml:"use_for,omitempty"`
}

// Profile is one routing policy.
type Profile struct {
	Description string                `yaml:"description,omitempty"`
	Roles       map[string]RoleTarget `yaml:"roles"`
}

// RoleTarget selects a template, with optional launch overrides.
type RoleTarget struct {
	Template string `yaml:"template"`
	Model    string `yaml:"model,omitempty"`
	Effort   string `yaml:"effort,omitempty"`
}

func (r *RoleTarget) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if value.Tag != "!!str" {
			return fmt.Errorf("role target must be a template string or mapping")
		}
		r.Template = value.Value
		r.Model, r.Effort = "", ""
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("role target must be a template string or mapping")
	}
	var raw map[string]yaml.Node
	if err := value.Decode(&raw); err != nil {
		return err
	}
	for key := range raw {
		if key != "template" && key != "model" && key != "effort" {
			return fmt.Errorf("unknown role target key %q", key)
		}
	}
	var target struct {
		Template string `yaml:"template"`
		Model    string `yaml:"model"`
		Effort   string `yaml:"effort"`
	}
	if err := value.Decode(&target); err != nil {
		return err
	}
	r.Template, r.Model, r.Effort = target.Template, target.Model, target.Effort
	return nil
}

func (r RoleTarget) MarshalYAML() (any, error) {
	if r.Model == "" && r.Effort == "" {
		return r.Template, nil
	}
	return struct {
		Template string `yaml:"template"`
		Model    string `yaml:"model,omitempty"`
		Effort   string `yaml:"effort,omitempty"`
	}{r.Template, r.Model, r.Effort}, nil
}

// Resolution is the resolved routing decision for one role.
type Resolution struct {
	Role, Profile, Template, Model, Effort string
}

// WithOverrides applies explicit dispatch values over profile values.
func (r Resolution) WithOverrides(model, effort string) Resolution {
	if model != "" {
		r.Model = model
	}
	if effort != "" {
		r.Effort = effort
	}
	return r
}

// ResolveRole performs an exact role lookup in the active profile.
func (c *Config) ResolveRole(role string) (Resolution, error) {
	if c.Delegation == nil {
		return Resolution{}, ErrDelegationDisabled
	}
	d := c.Delegation
	p, ok := d.Profiles[d.ActiveProfile]
	if !ok {
		return Resolution{}, fmt.Errorf("active delegation profile %q does not exist", d.ActiveProfile)
	}
	target, ok := p.Roles[role]
	if !ok {
		mapped := sortedRoleNames(p.Roles)
		return Resolution{}, fmt.Errorf("role %q is not mapped in active delegation profile %q (mapped: %s)", role, d.ActiveProfile, strings.Join(mapped, ", "))
	}
	return Resolution{Role: role, Profile: d.ActiveProfile, Template: target.Template, Model: target.Model, Effort: target.Effort}, nil
}

// DelegationWarnings reports mappings without a declared role. It intentionally
// does nothing when roles are omitted, because then profile roles are canonical.
func (c *Config) DelegationWarnings() []string {
	if c.Delegation == nil || c.Delegation.Roles == nil {
		return nil
	}
	var warnings []string
	for _, profileName := range sortedProfileNames(c.Delegation.Profiles) {
		for _, role := range sortedRoleNames(c.Delegation.Profiles[profileName].Roles) {
			if _, ok := c.Delegation.Roles[role]; !ok {
				warnings = append(warnings, fmt.Sprintf("delegation.profiles.%s.roles.%s is not declared in delegation.roles", profileName, role))
			}
		}
	}
	return warnings
}

func (c *Config) validateDelegation() []string {
	if c.Delegation == nil {
		return nil
	}
	d := c.Delegation
	var errs []string
	if d.ActiveProfile == "" {
		errs = append(errs, "delegation.active_profile is required")
	}
	if len(d.Profiles) == 0 {
		errs = append(errs, "delegation.profiles must not be empty")
	}
	if d.ActiveProfile != "" {
		if _, ok := d.Profiles[d.ActiveProfile]; !ok {
			errs = append(errs, fmt.Sprintf("delegation.active_profile %q does not exist", d.ActiveProfile))
		}
	}
	for role := range d.Roles {
		if !delegationNamePattern.MatchString(role) {
			errs = append(errs, fmt.Sprintf("delegation.roles.%q is not a valid name", role))
		}
	}
	for _, profileName := range sortedProfileNames(d.Profiles) {
		profile := d.Profiles[profileName]
		if !delegationNamePattern.MatchString(profileName) {
			errs = append(errs, fmt.Sprintf("delegation.profiles.%q is not a valid name", profileName))
		}
		for _, role := range sortedRoleNames(profile.Roles) {
			target := profile.Roles[role]
			prefix := fmt.Sprintf("delegation.profiles.%s.roles.%s", profileName, role)
			if !delegationNamePattern.MatchString(role) {
				errs = append(errs, fmt.Sprintf("%s is not a valid name", prefix))
				continue
			}
			if target.Template == "" {
				errs = append(errs, prefix+".template is required")
				continue
			}
			tmpl, ok := c.Templates[target.Template]
			if !ok {
				errs = append(errs, fmt.Sprintf("%s.template %q does not exist", prefix, target.Template))
				continue
			}
			h, err := harness.Get(c.TemplateHarness(tmpl))
			if err != nil {
				continue
			} // Existing validation reports the bad harness.
			if target.Model != "" {
				if err := h.ValidateModel(target.Model); err != nil {
					errs = append(errs, fmt.Sprintf("%s.model %v", prefix, err))
				}
			}
			if target.Effort != "" {
				validator, ok := h.(harness.EffortValidator)
				if !ok {
					errs = append(errs, fmt.Sprintf("%s.effort: effort not supported by harness %s", prefix, h.Name()))
				} else if err := validator.ValidateEffort(target.Effort); err != nil {
					errs = append(errs, fmt.Sprintf("%s.effort %v", prefix, err))
				}
			}
		}
	}
	if d.Roles != nil && d.ActiveProfile != "" {
		if profile, ok := d.Profiles[d.ActiveProfile]; ok {
			for _, role := range sortedRoleNames(d.Roles) {
				if _, ok := profile.Roles[role]; !ok {
					errs = append(errs, fmt.Sprintf("delegation.roles.%s is not mapped in active profile %s", role, d.ActiveProfile))
				}
			}
		}
	}
	return errs
}

// RenderDelegationInstructions returns the profile-independent prompt block.
func RenderDelegationInstructions(cfg *Config) string {
	if cfg == nil || cfg.Delegation == nil {
		return ""
	}
	d := cfg.Delegation
	roles := sortedRoleNames(d.Roles)
	if d.Roles == nil {
		if profile, ok := d.Profiles[d.ActiveProfile]; ok {
			roles = sortedRoleNames(profile.Roles)
		}
	}
	var b strings.Builder
	b.WriteString("Delegation roles:\n")
	for _, role := range roles {
		b.WriteString("- ")
		b.WriteString(role)
		if spec, ok := d.Roles[role]; ok && spec.UseFor != "" {
			b.WriteString(": ")
			b.WriteString(spec.UseFor)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Dispatch with `leo_dispatch(role: …)`; do not pick templates or models yourself.\n")
	b.WriteString("Call `leo_delegation` if a role you expect is missing.\n")
	return b.String()
}

// RenderDelegationStatus returns a deterministic current-profile routing table.
func RenderDelegationStatus(cfg *Config) string {
	if cfg == nil || cfg.Delegation == nil {
		return ""
	}
	d := cfg.Delegation
	profile, ok := d.Profiles[d.ActiveProfile]
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("profile\trole\ttemplate\tmodel\teffort\n")
	for _, role := range sortedRoleNames(profile.Roles) {
		t := profile.Roles[role]
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", d.ActiveProfile, role, t.Template, t.Model, t.Effort)
	}
	return b.String()
}

func sortedRoleNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func sortedProfileNames(m map[string]Profile) []string { return sortedRoleNames(m) }
