package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// routeTwins maps every config-affecting HTML route to the JSON route that
// does the same thing, so a browser feature cannot ship without its API (and
// so the app) following. A route with no JSON twin must say why.
var routeTwins = map[string]string{
	"POST /web/task/{name}/toggle":           "PUT /config/tasks/{name}",
	"POST /web/task/{name}/run":              "POST /tasks/{name}/run",
	"POST /web/config/reload":                "POST /config/reload",
	"POST /web/config/defaults":              "PUT /config/defaults",
	"POST /web/config/task/{name}":           "PUT /config/tasks/{name}",
	"POST /web/task/add":                     "POST /config/tasks",
	"DELETE /web/task/{name}/delete":         "DELETE /config/tasks/{name}",
	"GET /web/task/{name}/prompt":            "GET /config/tasks/{name}/prompt",
	"POST /web/task/{name}/prompt":           "PUT /config/tasks/{name}/prompt",
	"POST /web/config/template/{name}":       "PUT /config/templates/{name}",
	"POST /web/template/add":                 "POST /config/templates",
	"DELETE /web/template/{name}":            "DELETE /config/templates/{name}",
	"POST /web/template/{name}/rename":       "POST /config/templates/{name}/rename",
	"POST /web/template/{name}/override":     "POST /config/templates/{name}/override",
	"POST /web/environment/{name}/override":  "POST /config/environments/{name}/override",
	"POST /web/delegation/profile/override":  "POST /config/delegation/profiles/{p}/override",
	"POST /web/follow/sync":                  "POST /config/follow/sync",
	"POST /web/config/web":                   "PUT /config/web",
	"POST /web/config/client":                "PUT /config/client",
	"POST /web/config/host/{name}":           "PUT /config/hosts/{name}",
	"POST /web/host/add":                     "POST /config/hosts",
	"DELETE /web/host/{name}":                "DELETE /config/hosts/{name}",
	"POST /web/config/environment/{name}":    "PUT /config/environments/{name}",
	"POST /web/environment/add":              "POST /config/environments",
	"POST /web/environment/{name}/rename":    "POST /config/environments/{name}/rename",
	"DELETE /web/environment/{name}":         "DELETE /config/environments/{name}",
	"POST /web/delegation/cell":              "PUT /config/delegation/profiles/{p}/roles/{r}",
	"POST /web/delegation/use-for":           "PUT /config/delegation/roles/{r}",
	"POST /web/delegation/active":            "PUT /config/delegation/active",
	"POST /web/delegation/enabled":           "PUT /config/delegation/enabled",
	"POST /web/delegation/hide-agents":       "PUT /config/delegation/hide-agents",
	"GET /web/delegation/preview":            "GET /config/delegation/preview",
	"POST /web/delegation/role/add":          "POST /config/delegation/roles",
	"POST /web/delegation/role/rename":       "POST /config/delegation/roles/{r}/rename",
	"POST /web/delegation/role/delete":       "DELETE /config/delegation/roles/{r}",
	"POST /web/delegation/profile/add":       "POST /config/delegation/profiles",
	"POST /web/delegation/profile/rename":    "POST /config/delegation/profiles/{p}/rename",
	"POST /web/delegation/profile/delete":    "DELETE /config/delegation/profiles/{p}",
	"POST /web/delegation/profile/duplicate": "POST /config/delegation/profiles/{p}/duplicate",
	"POST /web/service/restart":              "POST /service/restart",
	"GET /web/service/logtail":               "GET /service/log",
	"POST /web/agents/restart":               "POST /service/restart-agents",
	"GET /web/cron/preview":                  "GET /cron/preview",
	"GET /partials/task/{name}/history":      "GET /tasks/{name}/history",
	"GET /partials/task/{name}/log":          "GET /tasks/{name}/log",
	"GET /web/partials/harness-options":      "n/a: the schema's harnesses document carries every harness's options",
	"GET /partials/status":                   "n/a: live state is GET /api/v1/state, not config",
}

// configHTMLPrefixes select the HTML routes that read or change configuration
// or operate the service; agent and dispatch routes are not config.
var configHTMLPrefixes = []string{
	"/web/config/", "/web/task", "/web/template", "/web/environment", "/web/host",
	"/web/delegation/", "/web/service/", "/web/agents/restart", "/web/cron/",
	"/web/partials/", "/partials/", "/web/follow/",
}

var htmlRoutePattern = regexp.MustCompile(`mux\.HandleFunc\("((?:GET|POST|PUT|DELETE) [^"]+)"`)

func registeredHTMLRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, m := range htmlRoutePattern.FindAllStringSubmatch(string(src), -1) {
		_, path, _ := strings.Cut(m[1], " ")
		for _, p := range configHTMLPrefixes {
			if strings.HasPrefix(path, p) {
				routes = append(routes, m[1])
				break
			}
		}
	}
	sort.Strings(routes)
	return routes
}

func TestEveryConfigWebRouteHasAJSONTwin(t *testing.T) {
	routes := registeredHTMLRoutes(t)
	if len(routes) < 30 {
		t.Fatalf("found only %d config web routes in web.go; the scan is broken", len(routes))
	}
	seen := map[string]bool{}
	for _, r := range routes {
		seen[r] = true
		if _, ok := routeTwins[r]; !ok {
			t.Errorf("web route %q has no row in routeTwins: add its JSON twin (or say why it has none)", r)
		}
	}
	for r := range routeTwins {
		if !seen[r] {
			t.Errorf("routeTwins lists %q, which web.go no longer registers", r)
		}
	}
}

func TestEveryRouteTwinIsRegistered(t *testing.T) {
	svc, _ := newConfigService(t, nil)
	mux := http.NewServeMux()
	svc.RegisterConfigRoutes(mux, "", func(h http.HandlerFunc) http.HandlerFunc { return h })
	svc.RegisterReloadRoute(mux, "", func(h http.HandlerFunc) http.HandlerFunc { return h })
	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	for html, twin := range routeTwins {
		if strings.HasPrefix(twin, "n/a") {
			continue
		}
		method, path, _ := strings.Cut(twin, " ")
		req := httptest.NewRequest(method, wildcard.ReplaceAllString(path, "x"), nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s: twin %q is not registered", html, twin)
		}
	}
}
