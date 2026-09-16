package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func (h *Hub) Proxy(w http.ResponseWriter, r *http.Request, name string) error {
	prefix := "/hosts/" + name
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == "" {
		path = "/"
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Path = path
	clone.URL = &u
	if name == "localhost" {
		if h.local == nil {
			return &HostError{Code: "host_unavailable", Message: "local daemon unavailable"}
		}
		h.local.ServeHTTP(w, clone)
		return nil
	}
	c, err := h.ensure(r.Context(), name)
	if err != nil {
		return err
	}
	target := &url.URL{Scheme: "http", Host: "daemon"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = c.transport
	attempt := c.attemptValue()
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, proxyErr error) {
		if req.Context().Err() == nil && !errors.Is(proxyErr, context.Canceled) {
			go c.markDown(attempt) // #nosec G118 -- actor notification must outlive the failed request
		}
		WriteError(w, http.StatusServiceUnavailable, &HostError{Code: "host_unavailable", Message: proxyErr.Error()})
	}
	proxy.ServeHTTP(w, clone)
	return nil
}

func WriteError(w http.ResponseWriter, status int, err error) {
	code := "host_unavailable"
	if he, ok := err.(*HostError); ok {
		code = he.Code
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "code": code, "error": err.Error()})
}
