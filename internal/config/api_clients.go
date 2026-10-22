package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// APIClientConfig scopes one external API client's bearer token.
//
// Unlike a template's permissions — where an empty allowlist means
// "unrestricted", the right default for an agent Leo spawned itself — a client
// token is held outside Leo's trust boundary. At least one scope is therefore
// required, and everything it does not name is denied. The scopes are
// independent: CanMessage starts agent turns, CanPost only drops text into a
// chat thread.
type APIClientConfig struct {
	CanMessage []string `yaml:"can_message,omitempty"`
	CanPost    []string `yaml:"can_post,omitempty"`
}

// APIClientTokenPath returns the token file for a named client, under the
// state directory. The name is validated by Validate before this is used, so
// it cannot escape the directory.
func APIClientTokenPath(homePath, name string) string {
	return filepath.Join(homePath, "state", "clients", name+".token")
}

// validateAPIClients reports every problem with the api_clients section.
// Errors are collected rather than returned on the first hit, matching the
// rest of Validate.
func validateAPIClients(clients map[string]APIClientConfig) []string {
	var errs []string
	for name, client := range clients {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, "api_clients has an entry with an empty name")
			continue
		}
		// The name becomes a filename under state/clients, and it is the
		// identity `from` is checked against — neither tolerates separators.
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." || strings.Contains(name, "#") {
			errs = append(errs, fmt.Sprintf("api_clients[%q] name must not contain '/', '\\' or '#'", name))
			continue
		}
		if len(client.CanMessage) == 0 && len(client.CanPost) == 0 {
			errs = append(errs, fmt.Sprintf("api_clients[%q] must set can_message or can_post; an empty scope can reach nothing", name))
			continue
		}
		errs = append(errs, validateClientPatterns(name, "can_message", client.CanMessage)...)
		errs = append(errs, validateClientPatterns(name, "can_post", client.CanPost)...)
	}
	return errs
}

// validateClientPatterns checks one scope list: no blank entries, and every
// entry a pattern the matcher accepts.
func validateClientPatterns(name, field string, patterns []string) []string {
	var errs []string
	for i, target := range patterns {
		if strings.TrimSpace(target) == "" {
			errs = append(errs, fmt.Sprintf("api_clients[%q].%s[%d] must not be empty", name, field, i))
			continue
		}
		if !leotools.ValidPattern(target) {
			errs = append(errs, fmt.Sprintf("api_clients[%q].%s[%d] %q is not a valid pattern", name, field, i, target))
		}
	}
	return errs
}
