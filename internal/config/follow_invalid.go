package config

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"gopkg.in/yaml.v3"
)

// InvalidEntryError reports a lead entry that cannot be used on this host.
// The entry is kept out of the effective config; anything that references
// it fails with this error and its reason.
type InvalidEntryError struct {
	Kind   EntryKind
	Name   string
	Lead   string
	Reason string
}

func (e *InvalidEntryError) Error() string {
	return fmt.Sprintf("%s %q from lead %q is invalid on this host: %s", e.Kind, e.Name, e.Lead, e.Reason)
}

// UnknownEntryError reports a name that is neither defined nor an invalid
// lead entry.
type UnknownEntryError struct {
	Kind EntryKind
	Name string
}

func (e *UnknownEntryError) Error() string { return fmt.Sprintf("unknown %s %q", e.Kind, e.Name) }

// FollowProbes are the host checks Load runs on lead entries (D4): the
// harness binary is on PATH, and an absolute harness config dir exists.
type FollowProbes struct {
	LookPath func(string) (string, error)
	Stat     func(string) (fs.FileInfo, error)
	// BinaryAdvisory turns a missing harness binary into a warning instead
	// of an invalid template. PATH only decides in the daemon, which
	// launches the harness; another process, such as a CLI run from a
	// non-interactive ssh shell, can have a different PATH.
	BinaryAdvisory bool
}

// followProbes outside the daemon; the daemon installs DaemonFollowProbes.
var followProbes = FollowProbes{LookPath: exec.LookPath, Stat: os.Stat, BinaryAdvisory: true}

// DaemonFollowProbes are the probes for the daemon process, where PATH is
// the one harnesses launch under, so the binary probe is strict.
func DaemonFollowProbes() FollowProbes {
	return FollowProbes{LookPath: exec.LookPath, Stat: os.Stat}
}

// SetFollowProbes replaces the D4 probes and returns a func restoring the
// previous ones. A test seam: Load runs at every config call site, so the
// probes cannot be injected per call.
func SetFollowProbes(p FollowProbes) (restore func()) {
	prev := followProbes
	followProbes = p
	return func() { followProbes = prev }
}

// configDirKeys are the environment keys naming a harness config dir.
var configDirKeys = []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"}

// maxDefaultsPasses bounds the D3 fix-up loop; each pass drops at least one
// key, and dropping one (say harness) can expose another (say model).
const maxDefaultsPasses = 8

// isolateInvalidLeadEntries moves every lead entry that fails this host's
// checks out of the effective config, recording its reason. Only entries
// whose source is the lead are checked; local entries fail Validate loudly.
// Environments go first, since the others reference them.
func (c *Config) isolateInvalidLeadEntries() {
	fl := c.follow
	fl.invalid = map[EntryKind]map[string]string{KindTemplate: {}, KindEnvironment: {}, KindProfile: {}}
	fl.invalidKeys = map[string]string{}
	for _, name := range c.leadNames(KindEnvironment) {
		errs := validateEnvironment(name, c.Environments[name])
		errs = append(errs, probeConfigDirs(name, c.Environments[name])...)
		if len(errs) > 0 {
			delete(c.Environments, name)
			fl.markInvalid(KindEnvironment, name, errs)
		}
	}
	c.dropInvalidLeadDefaults()
	hr := newHarnessResolver(c.DefaultsHarness())
	for _, name := range c.leadNames(KindTemplate) {
		tmpl := c.Templates[name]
		errs := c.validateTemplate(name, tmpl, hr)
		errs = append(errs, c.validateEnvironmentList("templates."+name+".environments", tmpl.Environments)...)
		if len(errs) == 0 {
			errs = fl.probeHarnessBinary(name, c.TemplateHarness(tmpl))
		}
		if len(errs) > 0 {
			delete(c.Templates, name)
			fl.markInvalid(KindTemplate, name, errs)
		}
	}
	for _, name := range c.leadNames(KindProfile) {
		if errs := c.validateProfile(name, c.Delegation.Profiles[name]); len(errs) > 0 {
			delete(c.Delegation.Profiles, name)
			fl.markInvalid(KindProfile, name, errs)
		}
	}
}

// markInvalid records a lead entry as invalid and drops its source: it is
// no longer an effective entry, so ownership checks and saves skip it.
func (fl *followLayer) markInvalid(kind EntryKind, name string, errs []string) {
	fl.invalid[kind][name] = strings.Join(errs, "; ")
	delete(fl.sources[kind], name)
}

// leadNames lists, sorted, the entries of kind whose source is the lead.
func (c *Config) leadNames(kind EntryKind) []string {
	var names []string
	for name, s := range c.follow.sources[kind] {
		if s == SourceLead {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// probeHarnessBinary checks a lead template's harness binary is on PATH:
// a failure invalidates the template, or only warns when the probe is
// advisory (outside the daemon).
func (fl *followLayer) probeHarnessBinary(name, harnessName string) []string {
	h, err := harness.Get(harnessName)
	if err != nil {
		return nil // validateTemplate reports it
	}
	if _, err := followProbes.LookPath(h.Binary()); err == nil {
		return nil
	}
	if followProbes.BinaryAdvisory {
		fl.warn("lead template %q: the %s binary %q is not on this process's PATH; the daemon checks its own PATH when it loads the config", name, h.Name(), h.Binary())
		return nil
	}
	return []string{fmt.Sprintf("templates.%s.harness: the %s binary %q is not on this host's PATH", name, h.Name(), h.Binary())}
}

// probeConfigDirs checks each harness config dir an environment names. Only
// a value that is an absolute path once ~ and variables expand is probed;
// one that cannot expand here is left to launch time.
func probeConfigDirs(name string, env map[string]string) []string {
	var errs []string
	for _, key := range configDirKeys {
		raw, ok := env[key]
		if !ok {
			continue
		}
		dir, err := Expander{}.Expand(raw)
		if err != nil || !filepath.IsAbs(dir) {
			continue
		}
		if _, err := followProbes.Stat(dir); err != nil {
			errs = append(errs, fmt.Sprintf("environments.%s.%s: %s does not exist on this host", name, key, dir))
		}
	}
	return errs
}

// dropInvalidLeadDefaults resets every lead-set defaults key that fails
// validation to its built-in default (D3), recording why. A failing key the
// follower set itself is left for Validate to report.
func (c *Config) dropInvalidLeadDefaults() {
	fl := c.follow
	for range maxDefaultsPasses {
		dropped := false
		for _, msg := range c.validateDefaults(newHarnessResolver(c.DefaultsHarness())) {
			key := defaultsKeyOf(msg)
			if key == "" || fl.isLocalDefaultKey(key) {
				continue
			}
			next, err := withoutDefaultsKey(c.Defaults, key)
			if err != nil {
				continue
			}
			c.Defaults = next
			fl.invalidKeys[key] = msg
			dropped = true
		}
		if !dropped {
			return
		}
	}
}

// defaultsKeyOf maps a "defaults.<path> ..." message to the key unit it
// concerns at D2 depth: "dispatch.viewer" for a viewer field, else the
// top-level key.
func defaultsKeyOf(msg string) string {
	path, ok := strings.CutPrefix(msg, "defaults.")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(path, " :["); i >= 0 {
		path = path[:i]
	}
	parts := strings.Split(path, ".")
	if followDeepKeys[parts[0]] && len(parts) > 1 {
		return parts[0] + "." + parts[1]
	}
	return parts[0]
}

func (fl *followLayer) isLocalDefaultKey(key string) bool {
	top, _, _ := strings.Cut(key, ".")
	return fl.localDefaultKeys[key] || fl.localDefaultKeys[top]
}

// withoutDefaultsKey returns d with the dotted key removed.
func withoutDefaultsKey(d DefaultsConfig, key string) (DefaultsConfig, error) {
	var n yaml.Node
	if err := n.Encode(d); err != nil {
		return d, err
	}
	top, sub, deep := strings.Cut(key, ".")
	if deep {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == top {
				n.Content[i+1] = withoutKey(n.Content[i+1], sub)
			}
		}
	} else {
		n = *withoutKey(&n, top)
	}
	var out DefaultsConfig
	err := n.Decode(&out)
	return out, err
}

func (c *Config) invalidReason(kind EntryKind, name string) (string, bool) {
	if c.follow == nil || c.follow.invalid == nil {
		return "", false
	}
	r, ok := c.follow.invalid[kind][name]
	return r, ok
}

// invalidEntry returns the error for an invalid lead entry, nil otherwise.
func (c *Config) invalidEntry(kind EntryKind, name string) error {
	if reason, ok := c.invalidReason(kind, name); ok {
		return &InvalidEntryError{Kind: kind, Name: name, Lead: c.follow.host, Reason: reason}
	}
	return nil
}

// isInvalidLead reports whether name is a lead entry isolated as invalid;
// existence checks treat such a name as known, so one bad lead entry does
// not cascade into everything that references it.
func (c *Config) isInvalidLead(kind EntryKind, name string) bool {
	_, ok := c.invalidReason(kind, name)
	return ok
}

// InvalidEntries maps each invalid lead entry of kind to its reason.
func (c *Config) InvalidEntries(kind EntryKind) map[string]string {
	if c.follow == nil {
		return nil
	}
	return maps.Clone(c.follow.invalid[kind])
}

// InvalidLeadDefaults maps each dropped lead defaults key to its reason.
func (c *Config) InvalidLeadDefaults() map[string]string {
	if c.follow == nil {
		return nil
	}
	return maps.Clone(c.follow.invalidKeys)
}

// LookupTemplate returns the named template, an *InvalidEntryError for an
// invalid lead template, or an *UnknownEntryError.
func (c *Config) LookupTemplate(name string) (TemplateConfig, error) {
	if tmpl, ok := c.Templates[name]; ok {
		return tmpl, nil
	}
	if err := c.invalidEntry(KindTemplate, name); err != nil {
		return TemplateConfig{}, err
	}
	return TemplateConfig{}, &UnknownEntryError{Kind: KindTemplate, Name: name}
}

// LookupEnvironment returns the named environment, an *InvalidEntryError
// for an invalid lead environment, or an *UnknownEnvironmentError.
func (c *Config) LookupEnvironment(name string) (map[string]string, error) {
	if env, ok := c.Environments[name]; ok {
		return env, nil
	}
	if err := c.invalidEntry(KindEnvironment, name); err != nil {
		return nil, err
	}
	return nil, &UnknownEnvironmentError{Name: name}
}

// LookupProfile returns the named delegation profile, an *InvalidEntryError
// for an invalid lead profile, or an *UnknownEntryError.
func (c *Config) LookupProfile(name string) (Profile, error) {
	if c.Delegation != nil {
		if p, ok := c.Delegation.Profiles[name]; ok {
			return p, nil
		}
	}
	if err := c.invalidEntry(KindProfile, name); err != nil {
		return Profile{}, err
	}
	return Profile{}, &UnknownEntryError{Kind: KindProfile, Name: name}
}
