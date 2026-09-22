package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web"
)

// A daemon IPC task toggle and a web delegation autosave are both
// load→mutate→save cycles on the same leo.yaml. They must serialize on one
// shared config writer or one of them silently drops the other's edit.
func TestTaskToggleAndDelegationAutosaveSerialize(t *testing.T) {
	const n = 12
	cfg := &config.Config{
		Web:       config.WebConfig{Enabled: true},
		Templates: map[string]config.TemplateConfig{"one": {}, "two": {}},
		Tasks:     map[string]config.TaskConfig{},
		Delegation: &config.DelegationConfig{
			Roles:         map[string]config.RoleSpec{},
			ActiveProfile: "p",
			Profiles:      map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{}}},
		},
	}
	for i := 0; i < n; i++ {
		cfg.Tasks[fmt.Sprintf("t%02d", i)] = config.TaskConfig{Schedule: "0 * * * *", PromptFile: "p.md", Enabled: false}
		role := fmt.Sprintf("r%02d", i)
		cfg.Delegation.Roles[role] = config.RoleSpec{}
		cfg.Delegation.Profiles["p"].Roles[role] = config.RoleTarget{Template: "one"}
	}
	cfgPath := filepath.Join(t.TempDir(), "leo.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	s, client := startTestServer(t, cfgPath)
	const port, token = 8391, "race-token"
	ws := web.New(cfgPath, &processAdapter{inner: s.processes}, s.scheduler, s, nil, web.Options{Port: port, APIToken: token, ConfigWriter: s.ConfigWriter()})
	handler := ws.Handler()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(TaskNameRequest{Name: fmt.Sprintf("t%02d", i)})
			resp, err := client.Post("http://localhost/task/enable", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Errorf("task enable: %v", err)
				return
			}
			resp.Body.Close()
		}(i)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/web/delegation/cell", strings.NewReader(fmt.Sprintf("profile=p&role=r%02d&template=two", i)))
			req.Host = fmt.Sprintf("127.0.0.1:%d", port)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if !strings.Contains(w.Body.String(), "saved") {
				t.Errorf("cell r%02d: %d %s", i, w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if task := loaded.Tasks[fmt.Sprintf("t%02d", i)]; !task.Enabled {
			t.Errorf("t%02d toggle lost", i)
		}
		if got := loaded.Delegation.Profiles["p"].Roles[fmt.Sprintf("r%02d", i)].Template; got != "two" {
			t.Errorf("r%02d autosave lost: %q", i, got)
		}
	}
}
