// Package secretenv resolves secret references in env values: an `env` entry
// whose value names an OS secret store item instead of holding the literal.
// Leo resolves a reference each time it spawns a process; config, saved
// files, follow-lead exports and agent records keep only the reference.
//
// Nothing here logs, and no error carries a resolved value or a backend's
// stdout.
package secretenv

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Backend names the secret store a Ref reads from.
type Backend string

// The supported backends.
const (
	Keychain      Backend = "keychain"       // macOS login keychain
	SecretService Backend = "secret-service" // Linux libsecret (secret-tool)
	SystemdCreds  Backend = "systemd-creds"  // $CREDENTIALS_DIRECTORY/<name>
	File          Backend = "file"           // a 0600 file owned by the user
	Command       Backend = "command"        // stdout of an argv, no shell
)

const backendList = "secret: keychain, secret-service, systemd-creds, file or command"

// Ref is a secret reference: the backend plus the selector fields it reads.
// Only the fields a backend reads may be set; Validate reports the rest.
type Ref struct {
	Backend    Backend           `yaml:"secret" json:"secret"`
	Service    string            `yaml:"service,omitempty" json:"service,omitempty"`
	Account    string            `yaml:"account,omitempty" json:"account,omitempty"`
	Attributes map[string]string `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	Name       string            `yaml:"name,omitempty" json:"name,omitempty"`
	Path       string            `yaml:"path,omitempty" json:"path,omitempty"`
	Argv       []string          `yaml:"argv,omitempty" json:"argv,omitempty"`
	// Unknown lists decoded keys no backend reads, for Validate to report.
	Unknown []string `yaml:"-" json:"-"`
}

// field is one selector field of a Ref: its key and whether it is set.
type field struct {
	key string
	set bool
}

func (r Ref) fields() []field {
	return []field{
		{"service", r.Service != ""},
		{"account", r.Account != ""},
		{"attributes", len(r.Attributes) > 0},
		{"name", r.Name != ""},
		{"path", r.Path != ""},
		{"argv", len(r.Argv) > 0},
	}
}

// allowedKeys are the selector fields each backend reads.
var allowedKeys = map[Backend][]string{
	Keychain:      {"service", "account"},
	SecretService: {"attributes"},
	SystemdCreds:  {"name"},
	File:          {"path"},
	Command:       {"argv"},
}

// Validate checks the reference's shape without any I/O: a known backend,
// its required keys present and non-empty, and no key it does not read.
func (r Ref) Validate() error {
	allowed, ok := allowedKeys[r.Backend]
	switch {
	case r.Backend == "":
		return fmt.Errorf("missing backend (%s)", backendList)
	case !ok:
		return fmt.Errorf("unknown backend %q (%s)", string(r.Backend), backendList)
	}
	var problems []string
	for _, k := range slices.Sorted(slices.Values(r.Unknown)) {
		problems = append(problems, fmt.Sprintf("unknown key %q", k))
	}
	for _, f := range r.fields() {
		if f.set && !slices.Contains(allowed, f.key) {
			problems = append(problems, fmt.Sprintf("%s is not valid for %s", f.key, r.Backend))
		}
	}
	problems = append(problems, r.requiredProblems()...)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (r Ref) requiredProblems() []string {
	switch r.Backend {
	case Keychain:
		if r.Service == "" {
			return []string{"keychain requires service"}
		}
	case SecretService:
		if len(r.Attributes) == 0 {
			return []string{"secret-service requires attributes"}
		}
		for k, v := range r.Attributes {
			if k == "" || v == "" {
				return []string{"attributes must have non-empty names and values"}
			}
		}
	case SystemdCreds:
		switch {
		case r.Name == "":
			return []string{"systemd-creds requires name"}
		case r.Name == "." || r.Name == ".." || strings.ContainsAny(r.Name, "/\x00"):
			return []string{"name must be a single path element"}
		}
	case File:
		if r.Path == "" {
			return []string{"file requires path"}
		}
	case Command:
		switch {
		case len(r.Argv) == 0:
			return []string{"command requires argv"}
		case r.Argv[0] == "":
			return []string{"argv[0] must not be empty"}
		}
	}
	return nil
}

// String is a deterministic, value-free descriptor of the reference, such as
// keychain(service=X, account=evan). Errors and display surfaces show it in
// place of a value.
func (r Ref) String() string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+quote(v))
		}
	}
	add("service", r.Service)
	add("account", r.Account)
	for _, k := range slices.Sorted(maps.Keys(r.Attributes)) {
		parts = append(parts, quote(k)+"="+quote(r.Attributes[k]))
	}
	add("name", r.Name)
	add("path", r.Path)
	if len(r.Argv) > 0 {
		quoted := make([]string, len(r.Argv))
		for i, a := range r.Argv {
			quoted[i] = quote(a)
		}
		parts = append(parts, "argv=["+strings.Join(quoted, " ")+"]")
	}
	return quote(string(r.Backend)) + "(" + strings.Join(parts, ", ") + ")"
}

// quote leaves a plain token bare and Go-quotes anything that could make the
// descriptor ambiguous or carry control characters into a log line.
func quote(s string) string {
	if s == "" || strings.ContainsFunc(s, func(c rune) bool {
		return c <= ' ' || c > '~' || strings.ContainsRune(`"\(),=[]`, c)
	}) {
		return strconv.Quote(s)
	}
	return s
}
