package consult

import "testing"

func TestDispatchRequestAndRecordCarryDelegationFields(t *testing.T) {
	req := Request{Role: "implement", Profile: "fast", Effort: "high"}
	if req.Role != "implement" || req.Profile != "fast" || req.Effort != "high" {
		t.Fatalf("request = %#v", req)
	}
	rec := Record{Role: req.Role, Profile: req.Profile, Effort: req.Effort}
	if rec.Role != "implement" || rec.Profile != "fast" || rec.Effort != "high" {
		t.Fatalf("record = %#v", rec)
	}
}
