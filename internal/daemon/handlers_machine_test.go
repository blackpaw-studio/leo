package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/machine"
)

type fakeMachineSampler struct{ stats machine.Stats }

func (f fakeMachineSampler) Sample() machine.Stats { return f.stats }

func startMachineTestServer(t *testing.T, opts ...Option) *http.Client {
	t.Helper()
	sockPath := tmpSockPath(t, "m.sock")
	s := New(sockPath, writeTestConfig(t, t.TempDir()), nil, opts...)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck
	return socketHTTPClient(sockPath)
}

func getMachine(t *testing.T, client *http.Client) (int, Response) {
	t.Helper()
	resp, err := client.Get("http://localhost/machine")
	if err != nil {
		t.Fatalf("GET /machine: %v", err)
	}
	defer resp.Body.Close()
	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return resp.StatusCode, body
}

func TestMachineRouteReturnsSpecShape(t *testing.T) {
	pct := 12.5
	client := startMachineTestServer(t, WithMachineStats(fakeMachineSampler{machine.Stats{
		SampledAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		CPU:       machine.CPU{Percent: &pct, Cores: 8},
		Memory:    &machine.Memory{UsedBytes: 4, TotalBytes: 16},
		Disk:      &machine.Disk{Path: "/state", UsedBytes: 10, TotalBytes: 100},
	}}))

	status, body := getMachine(t, client)

	if status != http.StatusOK || !body.OK {
		t.Fatalf("status = %d, ok = %v, error = %q", status, body.OK, body.Error)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body.Data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("keys = %v, want exactly sampled_at, cpu, memory, disk", got)
	}
	for _, key := range []string{"sampled_at", "cpu", "memory", "disk"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing key %q in %s", key, body.Data)
		}
	}
	if string(got["sampled_at"]) != `"2026-10-21T12:00:00Z"` {
		t.Fatalf("sampled_at = %s", got["sampled_at"])
	}
	if string(got["cpu"]) != `{"percent":12.5,"cores":8}` {
		t.Fatalf("cpu = %s", got["cpu"])
	}
	if string(got["memory"]) != `{"used_bytes":4,"total_bytes":16}` {
		t.Fatalf("memory = %s", got["memory"])
	}
	if string(got["disk"]) != `{"path":"/state","used_bytes":10,"total_bytes":100}` {
		t.Fatalf("disk = %s", got["disk"])
	}
}

func TestMachineRouteFailedSectionsAreLiteralNull(t *testing.T) {
	client := startMachineTestServer(t, WithMachineStats(fakeMachineSampler{machine.Stats{
		SampledAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		CPU:       machine.CPU{Cores: 4},
	}}))

	status, body := getMachine(t, client)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body.Data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["memory"]) != "null" || string(got["disk"]) != "null" {
		t.Fatalf("memory = %s, disk = %s, want literal null", got["memory"], got["disk"])
	}
	if string(got["cpu"]) != `{"percent":null,"cores":4}` {
		t.Fatalf("cpu = %s", got["cpu"])
	}
}

func TestMachineRouteWithoutSamplerIs503(t *testing.T) {
	client := startMachineTestServer(t)

	status, body := getMachine(t, client)

	if status != http.StatusServiceUnavailable || body.Code != ErrorCodeMachineStatsUnavailable {
		t.Fatalf("status = %d, code = %q, want 503 %q", status, body.Code, ErrorCodeMachineStatsUnavailable)
	}
}
