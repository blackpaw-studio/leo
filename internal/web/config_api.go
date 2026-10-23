package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maxConfigBody bounds a config API request body; the socket has no body-size
// middleware of its own.
const maxConfigBody = 1 << 20

// RegisterConfigRoutes mounts the operator-only JSON config API on mux under
// prefix. The daemon calls it twice with the same service: on the unix socket
// (prefix "", the 0600 socket is the operator's) and on the web API mux
// (prefix "/api/v1", wrap = requireOperatorToken). Both therefore serve
// identical bodies.
//
// POST /config/reload is not registered here: the socket already has its own
// route for it and the web API adds it separately (see RegisterReloadRoute).
func (c *ConfigService) RegisterConfigRoutes(mux *http.ServeMux, prefix string, wrap func(http.HandlerFunc) http.HandlerFunc) {
	c.configRoutes(func(pattern string, h http.HandlerFunc) {
		method, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(method+" "+prefix+path, wrap(h))
	})
}

// ConfigRoutePatterns lists every route the config API mounts under prefix,
// POST /config/reload included, as "METHOD /path" patterns. Tests walk it so a
// route added later is covered by the auth and parity checks without an edit.
func ConfigRoutePatterns(prefix string) []string {
	var patterns []string
	(&ConfigService{}).configRoutes(func(pattern string, _ http.HandlerFunc) {
		method, path, _ := strings.Cut(pattern, " ")
		patterns = append(patterns, method+" "+prefix+path)
	})
	return append(patterns, "POST "+prefix+"/config/reload")
}

// configRoutes declares the routes (everything but reload) against route.
func (c *ConfigService) configRoutes(route func(pattern string, h http.HandlerFunc)) {
	route("GET /config/schema", c.handleConfigSchema)
	c.registerDelegationRoutes(route)
	c.registerOpsRoutes(route)

	route("GET /config/{resource}", c.handleConfigGet)
	route("GET /config/{resource}/{name}", c.handleConfigGet)
	route("PUT /config/{resource}", c.handleConfigPut)
	route("PUT /config/{resource}/{name}", c.handleConfigPut)
	route("POST /config/{resource}", c.handleConfigAdd)
	route("POST /config/{resource}/{name}/rename", c.handleConfigRename)
	route("POST /config/{resource}/{name}/duplicate", c.handleConfigDuplicate)
	route("DELETE /config/{resource}", c.handleConfigDelete)
	route("DELETE /config/{resource}/{name}", c.handleConfigDelete)
}

// RegisterReloadRoute mounts POST /config/reload on mux under prefix.
func (c *ConfigService) RegisterReloadRoute(mux *http.ServeMux, prefix string, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST "+prefix+"/config/reload", wrap(c.handleConfigReloadAPI))
}

// writeConfigData writes a 2xx envelope.
func writeConfigData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, apiResponse{OK: true, Data: data})
}

// writeConfigError renders any error as the config API envelope: a
// *ConfigError keeps its status, code, fields and data; anything else is a 500.
func writeConfigError(w http.ResponseWriter, err error) {
	var ce *ConfigError
	if !errors.As(err, &ce) {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	resp := apiResponse{Error: ce.Message, Code: ce.Code, Errors: ce.Fields}
	if ce.Data != nil {
		resp.Data = ce.Data
	}
	writeJSON(w, ce.Status, resp)
}

// ifMatchOf reads the revision a write is conditional on. Every JSON write
// carries one; a missing header is 428 so no client can overwrite blindly.
func ifMatchOf(r *http.Request) (string, error) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	if v == "" || v == "*" {
		return "", errRevisionRequired()
	}
	return v, nil
}

// decodeConfigBody decodes a JSON body into dst, refusing unknown fields and
// oversized bodies; numbers stay json.Number so ints can be told from floats.
func decodeConfigBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBody))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errBadRequest("request body is required")
		}
		return errBadRequest("malformed JSON body: %v", err)
	}
	if dec.More() {
		return errBadRequest("malformed JSON body: trailing data")
	}
	return nil
}

// resourceOf resolves the {resource}/{name} path against the table: singletons
// take no name, collections require one where wantName.
func resourceOf(r *http.Request, wantName bool) (*configResource, string, error) {
	key, name := r.PathValue("resource"), r.PathValue("name")
	res := resourceByKey(key)
	if res == nil {
		return nil, "", errNotFound("unknown config resource %q", key)
	}
	if wantName && !res.Collection || !wantName && name != "" {
		return nil, "", errNotFound("%s has no item %q", res.Label, name)
	}
	return res, name, nil
}

func (c *ConfigService) handleConfigSchema(w http.ResponseWriter, _ *http.Request) {
	writeConfigData(w, http.StatusOK, c.buildSchemaDocument())
}

func (c *ConfigService) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	res, name, err := resourceOf(r, r.PathValue("name") != "")
	if err != nil {
		writeConfigError(w, err)
		return
	}
	doc, rev, err := c.readResource(res, name, r.PathValue("name") != "")
	if err != nil {
		writeConfigError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+rev+`"`)
	writeConfigData(w, http.StatusOK, doc)
}

// writeResponse is the body of every config write.
type writeResponse struct {
	Revision        string          `json:"revision"`
	RestartRequired RestartRequired `json:"restart_required"`
	Warning         string          `json:"warning,omitempty"`
	Item            any             `json:"item,omitempty"`
}

// respondWrite renders a successful write. The item document was built inside
// the write, so its revision is the response's.
func (c *ConfigService) respondWrite(w http.ResponseWriter, status int, res WriteResult) {
	w.Header().Set("ETag", `"`+res.Revision+`"`)
	writeConfigData(w, status, writeResponse{Revision: res.Revision, RestartRequired: res.Restart, Warning: res.Warning, Item: res.Item})
}

type putBody struct {
	Values map[string]any `json:"values"`
}

func (c *ConfigService) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	res, name, err := resourceOf(r, r.PathValue("name") != "")
	if err != nil {
		writeConfigError(w, err)
		return
	}
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	var body putBody
	if err := decodeConfigBody(w, r, &body); err != nil {
		writeConfigError(w, err)
		return
	}
	if body.Values == nil {
		writeConfigError(w, errBadRequest(`body must be {"values": {...}}`))
		return
	}
	result, err := c.ApplyPatch(ifMatch, res, name, body.Values)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	c.respondWrite(w, http.StatusOK, result)
}

type addBody struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
}

func (c *ConfigService) handleConfigAdd(w http.ResponseWriter, r *http.Request) {
	res, _, err := resourceOf(r, false)
	if err == nil && !res.has(actionAdd) {
		err = errNotFound("%s cannot be added to", res.Label)
	}
	if err != nil {
		writeConfigError(w, err)
		return
	}
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	var body addBody
	if err := decodeConfigBody(w, r, &body); err != nil {
		writeConfigError(w, err)
		return
	}
	result, err := c.AddItem(ifMatch, res, body.Name, map[string]string{"schedule": body.Schedule})
	if err != nil {
		writeConfigError(w, err)
		return
	}
	c.respondWrite(w, http.StatusCreated, result)
}

type renameBody struct {
	NewName string `json:"new_name"`
}

// handleConfigRename serves both rename and duplicate: the same body, the same
// preconditions, one action name apart.
func (c *ConfigService) handleConfigRename(w http.ResponseWriter, r *http.Request) {
	c.copyOrRename(w, r, actionRename, http.StatusOK, c.RenameItem)
}

func (c *ConfigService) handleConfigDuplicate(w http.ResponseWriter, r *http.Request) {
	c.copyOrRename(w, r, actionDuplicate, http.StatusCreated, c.DuplicateItem)
}

func (c *ConfigService) copyOrRename(w http.ResponseWriter, r *http.Request, action string, status int,
	do func(ifMatch string, res *configResource, name, newName string) (WriteResult, error)) {
	res, name, err := resourceOf(r, true)
	if err == nil && !res.has(action) {
		err = errNotFound("%s cannot be %sd", res.Label, action)
	}
	if err != nil {
		writeConfigError(w, err)
		return
	}
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	var body renameBody
	if err := decodeConfigBody(w, r, &body); err != nil {
		writeConfigError(w, err)
		return
	}
	result, err := do(ifMatch, res, name, body.NewName)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	c.respondWrite(w, status, result)
}

func (c *ConfigService) handleConfigDelete(w http.ResponseWriter, r *http.Request) {
	res, name, err := resourceOf(r, true)
	if err == nil && !res.has(actionDelete) {
		err = errNotFound("%s cannot be deleted", res.Label)
	}
	if err != nil {
		writeConfigError(w, err)
		return
	}
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	result, err := c.DeleteItem(ifMatch, res, name)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	c.respondWrite(w, http.StatusOK, result)
}
