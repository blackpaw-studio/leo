package web

import (
	"net/http"
	"testing"
)

func TestBriefConfigIsOffWhenAbsent(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/config/brief", "", nil)
	wantStatus(t, res, http.StatusOK)
	if on, _ := res.data()["values"].(map[string]any)["enabled"].(bool); on {
		t.Errorf("values = %v, want enabled false", res.data()["values"])
	}
	if h.loadConfig().Brief.Enabled {
		t.Error("an absent brief block must read as off")
	}
}

func TestBriefConfigRoundTripsThroughPutWithIfMatch(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.put("/config/brief", map[string]any{"enabled": true}), http.StatusOK)
	if !h.loadConfig().Brief.Enabled {
		t.Fatal("brief.enabled not persisted")
	}
	res := h.do(http.MethodGet, "/config/brief", "", nil)
	if on, _ := res.data()["values"].(map[string]any)["enabled"].(bool); !on {
		t.Errorf("values = %v", res.data()["values"])
	}
	wantStatus(t, h.put("/config/brief", map[string]any{"enabled": false}), http.StatusOK)
	if h.loadConfig().Brief.Enabled {
		t.Error("brief.enabled not cleared")
	}
}

func TestBriefConfigWriteNeedsAFreshRevision(t *testing.T) {
	h := newAPIHarness(t)
	body := map[string]any{"values": map[string]any{"enabled": true}}
	wantStatus(t, h.do(http.MethodPut, "/config/brief", "", body), http.StatusPreconditionRequired)
	stale := h.revision()
	wantStatus(t, h.put("/config/defaults", map[string]any{"max_turns": 11}), http.StatusOK)
	res := h.do(http.MethodPut, "/config/brief", stale, body)
	wantStatus(t, res, http.StatusConflict)
	if h.loadConfig().Brief.Enabled {
		t.Error("a stale write must not land")
	}
}

func TestBriefConfigChangeNeedsNoRestart(t *testing.T) {
	h := newAPIHarness(t)
	rr := h.put("/config/brief", map[string]any{"enabled": true}).data()["restart_required"].(map[string]any)
	if rr["service"] != false || rr["agents"] != false {
		t.Errorf("restart_required = %v", rr)
	}
}
