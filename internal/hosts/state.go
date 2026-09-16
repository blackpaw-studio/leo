package hosts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

func (h *Hub) RemoteAgents(ctx context.Context) []any {
	out := []any{}
	h.mu.RLock()
	cs := make([]*connection, 0, len(h.conns))
	for _, c := range h.conns {
		if c.stateValue() == StateConnected {
			cs = append(cs, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range cs {
		out = append(out, fetchAgents(ctx, c.name, c.transport)...)
	}
	return out
}

func fetchAgents(ctx context.Context, name string, tr *http.Transport) []any {
	client := &http.Client{Transport: tr}
	body, status := getBody(ctx, client, "/state?scope=local")
	var agents []map[string]any
	if status == http.StatusOK {
		var env struct {
			Data struct {
				Agents []map[string]any `json:"agents"`
			} `json:"data"`
		}
		if json.NewDecoder(bytes.NewReader(body)).Decode(&env) == nil {
			agents = env.Data.Agents
		}
	}
	if agents == nil {
		body, _ = getBody(ctx, client, "/agents/list")
		var env struct {
			Data []map[string]any `json:"data"`
		}
		_ = json.NewDecoder(bytes.NewReader(body)).Decode(&env)
		agents = env.Data
	}
	out := make([]any, 0, len(agents))
	for _, a := range agents {
		if host, ok := a["host"].(string); ok && host != "" && host != "localhost" {
			continue
		}
		a["host"] = name
		out = append(out, a)
	}
	return out
}

func getBody(ctx context.Context, client *http.Client, path string) ([]byte, int) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://daemon"+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	return body.Bytes(), resp.StatusCode
}
