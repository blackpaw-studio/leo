package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// Stable error classes of the JSON config API (docs/configuration/config-api.md).
const (
	CodeBadRequest          = "bad_request"
	CodeNotFound            = "not_found"
	CodeDelegationMissing   = "delegation_not_configured"
	CodeRevisionMismatch    = "revision_mismatch"
	CodeInvalid             = "invalid"
	CodeRevisionRequired    = "revision_required"
	messageNotFoundFallback = "Not found"
)

// ConfigError is a failure of a ConfigService core, carrying everything the
// JSON adapter needs (status, class, per-field errors, extra data) while the
// HTML adapter only reads Error() — the text the web flashes always showed.
type ConfigError struct {
	Status  int
	Code    string
	Message string
	// Fields are the per-field problems of a 422; field "" is form-level.
	Fields []schema.Problem
	// Data rides in the envelope's data (the current revision on a 409).
	Data any
}

func (e *ConfigError) Error() string { return e.Message }

func errNotFound(format string, args ...any) *ConfigError {
	return &ConfigError{Status: http.StatusNotFound, Code: CodeNotFound, Message: fmt.Sprintf(format, args...)}
}

func errBadRequest(format string, args ...any) *ConfigError {
	return &ConfigError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: fmt.Sprintf(format, args...)}
}

// errInvalid is a 422 pinned to one field ("" for form-level).
func errInvalid(field, message string) *ConfigError {
	return &ConfigError{Status: http.StatusUnprocessableEntity, Code: CodeInvalid, Message: message,
		Fields: []schema.Problem{{Field: field, Message: message}}}
}

// errInvalidFields is a 422 over several problems.
func errInvalidFields(problems []schema.Problem) *ConfigError {
	msgs := make([]string, 0, len(problems))
	for _, p := range problems {
		msgs = append(msgs, p.Message)
	}
	return &ConfigError{Status: http.StatusUnprocessableEntity, Code: CodeInvalid, Message: strings.Join(msgs, "; "), Fields: problems}
}

func errConflict(current string) *ConfigError {
	return &ConfigError{Status: http.StatusConflict, Code: CodeRevisionMismatch,
		Message: "leo.yaml changed since it was read", Data: map[string]string{"revision": current}}
}

func errRevisionRequired() *ConfigError {
	return &ConfigError{Status: http.StatusPreconditionRequired, Code: CodeRevisionRequired,
		Message: "If-Match with the current revision is required"}
}

func configUnavailable(err error) *ConfigError {
	return &ConfigError{Status: http.StatusServiceUnavailable, Code: CodeConfigUnavailable,
		Message: fmt.Sprintf("Failed to load config: %v", err)}
}

// validationScope names the resource a write edits, so a Validate message
// about it lands on its field while a message about anything else (another
// resource already invalid on disk) stays form-level.
type validationScope struct {
	Resource string // "defaults", "templates", "tasks", "environments", "web", "client", "hosts"
	Name     string // the item, for collections
	// Profile and Role narrow "delegation" to one profile × role cell, whose
	// fields are template, model and effort.
	Profile string
	Role    string
}

// optionKeyPattern pulls the offending key out of a harness DecodeOptions
// message such as `option "bypass_permissions" must be a boolean`.
var optionKeyPattern = regexp.MustCompile(`option "([A-Za-z0-9_]+)"`)

// fieldProblems maps Validate's messages to per-field problems for scope. A
// message is "<dotted path> <detail>"; it belongs to the edited resource when
// the path starts with the resource's prefix and the next segment is one of
// its field keys. Everything else is form-level.
func fieldProblems(scope validationScope, messages []string, fields func(resource string) map[string]bool) []schema.Problem {
	prefix, single := scopePrefix(scope)
	known := fields(scope.Resource)
	problems := make([]schema.Problem, 0, len(messages))
	for _, msg := range messages {
		problems = append(problems, schema.Problem{Field: fieldOfMessage(prefix, single, known, msg), Message: msg})
	}
	return problems
}

// scopePrefix is the dotted path a resource's messages start with. single is
// set for a resource whose only field is "env" (environments), whose messages
// name no field.
func scopePrefix(scope validationScope) (prefix string, single string) {
	switch scope.Resource {
	case "defaults":
		return "defaults.", ""
	case "templates":
		return "templates." + scope.Name + ".", ""
	case "tasks":
		return "tasks." + scope.Name + ".", ""
	case "environments":
		return "environments." + scope.Name, "env"
	case "web":
		return "web.", ""
	case "hosts":
		return "client.hosts." + scope.Name + ".", ""
	case "client":
		return "client.", ""
	case "delegation":
		if scope.Profile != "" && scope.Role != "" {
			return "delegation.profiles." + scope.Profile + ".roles." + scope.Role + ".", ""
		}
		return "delegation.", ""
	}
	return "", ""
}

func fieldOfMessage(prefix, single string, known map[string]bool, msg string) string {
	if prefix == "" || !strings.HasPrefix(msg, prefix) {
		return ""
	}
	if single != "" {
		return single
	}
	rest := msg[len(prefix):]
	end := strings.IndexAny(rest, " :[")
	if end < 0 {
		end = len(rest)
	}
	key := rest[:end]
	if strings.HasPrefix(key, "harness_options") {
		if m := optionKeyPattern.FindStringSubmatch(msg); m != nil {
			return schema.HarnessOptionPrefix + m[1]
		}
		return "harness_options"
	}
	if known[key] {
		return key
	}
	if sub, _, ok := strings.Cut(key, "."); ok && known[sub] {
		return key
	}
	return ""
}
