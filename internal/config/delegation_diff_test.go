package config

import "testing"

func TestDiffProfiles(t *testing.T) {
	got := DiffProfiles(Profile{Roles: map[string]RoleTarget{"b": {Template: "one"}, "a": {Template: "one"}}}, Profile{Roles: map[string]RoleTarget{"b": {Template: "two"}, "c": {Template: "one"}}})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("diff=%v", got)
	}
}
