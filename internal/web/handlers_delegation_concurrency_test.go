package web

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Concurrent autosaves of different fields must all persist: each save is a
// load→mutate→save, so without serialization a later save can clobber an
// earlier one while both cells report "saved".
func TestDelegationConcurrentAutosavesKeepEveryEdit(t *testing.T) {
	const roles = 24
	cfg := delegationTestConfig()
	p := cfg.Delegation.Profiles["p"]
	for i := 0; i < roles; i++ {
		name := fmt.Sprintf("r%02d", i)
		cfg.Delegation.Roles[name] = config.RoleSpec{}
		p.Roles[name] = config.RoleTarget{Template: "one"}
	}
	cfg.Delegation.Profiles["p"] = p
	s, path := newTestServerWithConfigFile(t, cfg)

	var wg sync.WaitGroup
	for i := 0; i < roles; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("r%02d", i)
			if i%2 == 0 {
				w := postDelegation(t, s.handleDelegationUseFor, "role="+name+"&use_for=note-"+name)
				if !strings.Contains(w.Body.String(), "saved") {
					t.Errorf("%s: %s", name, w.Body.String())
				}
				return
			}
			w := postDelegation(t, s.handleDelegationCell, "profile=p&role="+name+"&template=two")
			if !strings.Contains(w.Body.String(), "saved") {
				t.Errorf("%s: %s", name, w.Body.String())
			}
		}(i)
	}
	wg.Wait()

	loaded := loadConfigFile(t, path)
	for i := 0; i < roles; i++ {
		name := fmt.Sprintf("r%02d", i)
		if i%2 == 0 {
			if got := loaded.Delegation.Roles[name].UseFor; got != "note-"+name {
				t.Errorf("%s use_for lost: %q", name, got)
			}
		} else if got := loaded.Delegation.Profiles["p"].Roles[name].Template; got != "two" {
			t.Errorf("%s template lost: %q", name, got)
		}
	}
}
