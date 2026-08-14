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
// token is held outside Leo's trust boundary. CanMessage is therefore
// required, and everything it does not name is denied.
type APIClientConfig struct {
	CanMessage []string `yaml:"can_message"`
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
		if len(client.CanMessage) == 0 {
			errs = append(errs, fmt.Sprintf("api_clients[%q] must set can_message; an empty list can message nothing", name))
			continue
		}
		for i, target := range client.CanMessage {
			if strings.TrimSpace(target) == "" {
				errs = append(errs, fmt.Sprintf("api_clients[%q].can_message[%d] must not be empty", name, i))
				continue
			}
			if !leotools.ValidPattern(target) {
				errs = append(errs, fmt.Sprintf("api_clients[%q].can_message[%d] %q is not a valid pattern", name, i, target))
			}
		}
	}
	return errs
}
