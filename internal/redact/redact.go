// Package redact masks secret-looking configuration values before they are
// rendered into a place an agent can read — an MCP tool result, a CLI dump, a
// log line. Agent env and template env routinely carry live credentials
// (OP_SERVICE_ACCOUNT_TOKEN, ANTHROPIC_AUTH_TOKEN), and an innocuous-looking
// listing call must not deposit them into a transcript.
//
// Matching is a denylist over key names, not values: agent env is
// user-extensible, so an allowlist would silently leak every key leo has not
// heard of yet. Over-redaction (masking a harmless AWS_REGION) is the
// intended failure direction.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// Mask is what a redacted value renders as.
const Mask = "<redacted>"

// secretKeyFragments match anywhere in the upper-cased key.
var secretKeyFragments = []string{
	"TOKEN",
	"SECRET",
	"PASSWORD",
	"PASSWD",
	"PASS", // DB_PASS
	"PWD",  // SMTP_PWD
	"CREDENTIAL",
	"AUTH",
	"COOKIE",
	"SESSION",   // session ids are bearer credentials
	"SIGNATURE", // request-signing material
	"DSN",       // SENTRY_DSN embeds a key
	"WEBHOOK",   // webhook URLs are unguessable-by-design
	"KEY",       // API_KEY, PRIVATE_KEY, ACCESS_KEY, SIGNING_KEY, …
}

// credentialedURL matches a URL whose authority carries inline credentials
// (scheme://user:pass@host). Catches the case a key denylist cannot: an
// innocuous name like DATABASE_URL or MONGO_URI holding a live password.
var credentialedURL = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^/@\s]*:[^/@\s]*@`)

// secretKeyPrefixes match whole credential namespaces, where even the
// non-obvious keys are worth withholding.
var secretKeyPrefixes = []string{
	"OP_",  // 1Password service accounts / connect
	"AWS_", // AWS credential + session namespace
}

// IsSecretKey reports whether an env key's name suggests its value is a
// credential.
func IsSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, prefix := range secretKeyPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	for _, fragment := range secretKeyFragments {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

// Value returns val, or Mask when key names a credential or val is a URL with
// inline credentials. An empty value is returned as-is: there is nothing to
// leak, and masking it would imply the key is set to something.
func Value(key, val string) string {
	if val == "" {
		return val
	}
	if !IsSecretKey(key) && !credentialedURL.MatchString(val) {
		return val
	}
	return Mask
}

// EnvMap returns a copy of env with every secret-looking value masked. Nil in,
// nil out; the input is never mutated.
func EnvMap(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	safe := make(map[string]string, len(env))
	for k, v := range env {
		safe[k] = Value(k, v)
	}
	return safe
}

// Keys returns env's key names, sorted. Useful where callers want to know
// which env a scope defines without any of the values crossing the boundary.
func Keys(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
