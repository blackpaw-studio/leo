package consult

import "testing"

func TestMatchGrant(t *testing.T) {
	grants := []string{"agent:leoterm", "template:marketing", "task:weekly-digest"}
	tests := []struct {
		name      string
		grants    []string
		root      RootIdentity
		apiClient bool
		want      string
		wantOK    bool
	}{
		{name: "agent by name", grants: grants, root: RootIdentity{Caller: "leoterm", Template: "coder"}, want: "agent:leoterm", wantOK: true},
		{name: "template uses spawned-from template", grants: grants, root: RootIdentity{Caller: "mk-1", Template: "marketing"}, want: "template:marketing", wantOK: true},
		{name: "task caller", grants: grants, root: RootIdentity{Caller: "task:weekly-digest"}, want: "task:weekly-digest", wantOK: true},
		{name: "first match in list order", grants: []string{"template:coder", "agent:leoterm"}, root: RootIdentity{Caller: "leoterm", Template: "coder"}, want: "template:coder", wantOK: true},
		{name: "task entry does not match same-named agent", grants: []string{"task:digest"}, root: RootIdentity{Caller: "digest", Template: "digest"}},
		{name: "agent entry does not match task caller", grants: []string{"agent:task:digest", "agent:digest"}, root: RootIdentity{Caller: "task:digest"}},
		{name: "template entry does not match agent name", grants: []string{"template:leoterm"}, root: RootIdentity{Caller: "leoterm", Template: "coder"}},
		{name: "empty template never matches", grants: []string{"template:"}, root: RootIdentity{Caller: "x"}},
		{name: "no entry refused", grants: nil, root: RootIdentity{Caller: "leoterm", Template: "coder"}},
		{name: "empty caller exempt", grants: nil, root: RootIdentity{}, want: "caller:none", wantOK: true},
		{name: "api client exempt", grants: nil, root: RootIdentity{Caller: "leoterm-ios"}, apiClient: true, want: "api_client:leoterm-ios", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := matchGrant(tt.grants, tt.root, tt.apiClient)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("matchGrant = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
