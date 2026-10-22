package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/machine"
)

type fakeMachineSampler struct{ stats machine.Stats }

func (f fakeMachineSampler) Sample() machine.Stats { return f.stats }

func TestAPIMachineReturnsSpecShapeInEnvelope(t *testing.T) {
	s := newClientTokenServer(t)
	pct := 12.5
	s.machineStats = fakeMachineSampler{machine.Stats{
		SampledAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		CPU:       machine.CPU{Percent: &pct, Cores: 8},
		Memory:    &machine.Memory{UsedBytes: 4, TotalBytes: 16},
	}}

	w := requestAs(t, s, "GET", "/api/v1/machine", testAPIToken, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var env struct {
		OK   bool                       `json:"ok"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK || len(env.Data) != 4 {
		t.Fatalf("envelope = %s, want ok with exactly sampled_at, cpu, memory, disk", w.Body.String())
	}
	if string(env.Data["sampled_at"]) != `"2026-10-21T12:00:00Z"` ||
		string(env.Data["cpu"]) != `{"percent":12.5,"cores":8}` ||
		string(env.Data["memory"]) != `{"used_bytes":4,"total_bytes":16}` {
		t.Fatalf("data = %v", env.Data)
	}
	if string(env.Data["disk"]) != "null" {
		t.Fatalf("disk = %s, want literal null", env.Data["disk"])
	}
}

func TestAPIMachineWithoutSamplerIs503(t *testing.T) {
	s := newClientTokenServer(t)

	w := requestAs(t, s, "GET", "/api/v1/machine", testAPIToken, "")

	var env apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusServiceUnavailable || env.Code != CodeMachineStatsUnavailable {
		t.Fatalf("status = %d, code = %q, want 503 %q", w.Code, env.Code, CodeMachineStatsUnavailable)
	}
}

// /api/v1/machine sits behind exactly the auth /api/v1/state does.
func TestAPIMachineAuthMatchesState(t *testing.T) {
	s := newClientTokenServer(t)
	s.machineStats = fakeMachineSampler{machine.Stats{CPU: machine.CPU{Cores: 1}}}

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"unknown token", "nope", http.StatusUnauthorized},
		{"operator token", testAPIToken, http.StatusOK},
		{"agent token", testAgentToken, http.StatusOK},
		{"api client token", testClientToken, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machineCode := requestAs(t, s, "GET", "/api/v1/machine", tt.token, "").Code
			stateCode := requestAs(t, s, "GET", "/api/v1/state", tt.token, "").Code

			if machineCode != tt.want {
				t.Errorf("/api/v1/machine = %d, want %d", machineCode, tt.want)
			}
			if machineCode != stateCode {
				t.Errorf("/api/v1/machine = %d but /api/v1/state = %d", machineCode, stateCode)
			}
		})
	}
}
