package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// hostAPIScope is the scope under which a client host's secret reference is
// reported (validation messages, `leo validate`).
func hostAPIScope(host string) string { return "client.hosts." + host }

// validAPIClientName reports whether name is usable as an api_clients
// identity: non-empty and free of path separators and the '#' that splits a
// client name from its session suffix.
func validAPIClientName(name string) bool {
	return strings.TrimSpace(name) != "" &&
		!strings.ContainsAny(name, `/\#`) && name != "." && name != ".."
}

// validateClientHosts reports every problem with the API fields of
// client.hosts (api_url, api_token, api_client). Errors never carry the
// token's value.
func validateClientHosts(hosts map[string]HostConfig) []string {
	var errs []string
	for _, name := range sortedKeys(hosts) {
		h := hosts[name]
		scope := hostAPIScope(name)
		hasURL := h.APIURL != ""
		hasToken := h.APIToken != nil && h.APIToken.String() != ""
		if !hasURL && h.APIToken == nil && h.APIClient == "" {
			continue
		}
		if hasURL != hasToken {
			errs = append(errs, fmt.Sprintf("%s: api_url and api_token must be set together", scope))
		}
		if hasURL {
			errs = append(errs, validateHostAPIURL(scope, h.APIURL)...)
			if strings.Contains(name, "@") {
				errs = append(errs, fmt.Sprintf("%s: host name %q must not contain '@' when api_url is set (agent@host addressing)", scope, name))
			}
		}
		if h.APIToken != nil {
			if r, ok := h.APIToken.Ref(); ok {
				if err := r.Validate(); err != nil {
					errs = append(errs, fmt.Sprintf("%s.api_token: secret: %v", scope, err))
				}
			}
		}
		if h.APIClient != "" && (!validAPIClientName(h.APIClient) || strings.Contains(h.APIClient, "@")) {
			errs = append(errs, fmt.Sprintf("%s.api_client %q must be a valid api_clients name (no '/', '\\', '#' or '@')", scope, h.APIClient))
		}
	}
	return errs
}

func validateHostAPIURL(scope, raw string) []string {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return []string{fmt.Sprintf("%s.api_url is not a valid URL", scope)}
	case u.Scheme != "http" && u.Scheme != "https":
		return []string{fmt.Sprintf("%s.api_url must use http or https", scope)}
	case u.Hostname() == "":
		return []string{fmt.Sprintf("%s.api_url must include a host", scope)}
	case u.User != nil:
		// The message names no part of the URL: userinfo may hold a credential.
		return []string{fmt.Sprintf("%s.api_url must not contain credentials", scope)}
	case u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#"):
		return []string{fmt.Sprintf("%s.api_url must not contain a query or fragment", scope)}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	})
}
