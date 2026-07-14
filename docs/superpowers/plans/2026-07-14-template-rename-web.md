# Template Rename (Web UI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user rename an agent template from the web UI, cascading the new name to every task that references it and to persisted agent records' template pointers.

**Architecture:** A pure `config.RenameTemplate` helper re-keys the template map and rewrites referencing tasks. A web handler wires that helper to `validateAndSave`, then best-effort updates agentstore `Record.Template` pointers, and `HX-Redirect`s to the renamed edit page. A small rename form on the template edit page drives it.

**Tech Stack:** Go, `net/http` (stdlib routing), htmx, Go `html/template`.

## Global Constraints

- Config package must **not** import the `web` package. Name-shape validation (`validEntityName`) stays in the web layer; `config.RenameTemplate` guards only structural invariants.
- Agents keep their spawn-time identity: only `agentstore.Record.Template` moves. Do not rename tmux sessions or agentstore keys.
- Best-effort agentstore cascade: a failed record update logs and continues; it does not fail the rename.
- Follow existing patterns: mutate the loaded `cfg` then `validateAndSave` (like `handleTemplateAdd`/`handleTemplateDelete`); error path mirrors `handleWebAgentRename` (`renderFlashToContainer`).
- `make test` is `go test -race -cover ./...`.

---

### Task 1: `config.RenameTemplate` helper

**Files:**
- Create: `internal/config/rename.go`
- Test: `internal/config/rename_test.go`

**Interfaces:**
- Consumes: `Config` (`internal/config/config.go` — `Tasks map[string]TaskConfig`, `Templates map[string]TemplateConfig`; `TaskConfig.Template string`).
- Produces: `func RenameTemplate(cfg *Config, oldName, newName string) error` — mutates `cfg` in place.

- [ ] **Step 1: Write the failing test**

Create `internal/config/rename_test.go`:

```go
package config

import "testing"

func TestRenameTemplate_ReKeysAndRewritesTaskRefs(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"old":   {Model: "sonnet"},
			"other": {Model: "opus"},
		},
		Tasks: map[string]TaskConfig{
			"t1": {Runtime: "persistent", Template: "old"},
			"t2": {Runtime: "persistent", Template: "other"},
		},
	}

	if err := RenameTemplate(cfg, "old", "new"); err != nil {
		t.Fatalf("RenameTemplate: %v", err)
	}

	if _, ok := cfg.Templates["old"]; ok {
		t.Error("old template key still present")
	}
	if got := cfg.Templates["new"].Model; got != "sonnet" {
		t.Errorf("new template Model = %q, want sonnet", got)
	}
	if got := cfg.Tasks["t1"].Template; got != "new" {
		t.Errorf("t1.Template = %q, want new", got)
	}
	if got := cfg.Tasks["t2"].Template; got != "other" {
		t.Errorf("t2.Template = %q, want other (unchanged)", got)
	}
}

func TestRenameTemplate_Errors(t *testing.T) {
	tests := []struct {
		name             string
		oldName, newName string
	}{
		{"empty new name", "old", ""},
		{"old missing", "missing", "new"},
		{"new collides", "old", "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Templates: map[string]TemplateConfig{
				"old":   {Model: "sonnet"},
				"other": {Model: "opus"},
			}}
			if err := RenameTemplate(cfg, tc.oldName, tc.newName); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestRenameTemplate ./internal/config/`
Expected: FAIL — `undefined: RenameTemplate`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/config/rename.go`:

```go
package config

import "fmt"

// RenameTemplate re-keys a template from oldName to newName within cfg and
// rewrites every task that targets the old name, so no reference is left
// dangling. It mutates cfg in place.
//
// Name-shape validation is the caller's responsibility (the web layer applies
// validEntityName before calling this, and the config package must not import
// web). RenameTemplate guards only the structural invariants it can see:
// non-empty new name, old exists, new does not collide.
func RenameTemplate(cfg *Config, oldName, newName string) error {
	if newName == "" {
		return fmt.Errorf("new template name must not be empty")
	}
	tmpl, ok := cfg.Templates[oldName]
	if !ok {
		return fmt.Errorf("template %q not found", oldName)
	}
	if _, exists := cfg.Templates[newName]; exists {
		return fmt.Errorf("template %q already exists", newName)
	}

	cfg.Templates[newName] = tmpl
	delete(cfg.Templates, oldName)

	for name, task := range cfg.Tasks {
		if task.Template == oldName {
			task.Template = newName
			cfg.Tasks[name] = task
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race -run TestRenameTemplate ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/rename.go internal/config/rename_test.go
git commit -m "feat(config): add RenameTemplate helper that cascades to task refs"
```

---

### Task 2: Web handler, route, and agentstore cascade

**Files:**
- Modify: `internal/web/handlers.go` (add `handleTemplateRename` after `handleTemplateDelete` ~line 771; add `agentstore` import)
- Modify: `internal/web/web.go` (register route ~line 234, next to the other `/web/template/...` routes)
- Test: `internal/web/handlers_templates_test.go`

**Interfaces:**
- Consumes: `config.RenameTemplate` (Task 1); `s.loadConfig()`, `s.validateAndSave(cfg)`, `s.reloadConfigOrWarn()`, `validEntityName`, `entityNameError`, `renderFlashToContainer` (all in `internal/web`); `agentstore.Load`, `agentstore.FilePath`, `agentstore.Update` (`internal/agentstore/store.go`); `cfg.HomePath`.
- Produces: `POST /web/template/{name}/rename` → `handleTemplateRename`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/handlers_templates_test.go`:

```go
func TestTemplateRenameSuccess(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)

	// Seed an agentstore record spawned from "coding" so we can assert the
	// pointer cascade.
	if err := agentstore.Save(dir, agentstore.Record{Name: "leo-coding-leo", Template: "coding"}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}

	form := url.Values{"new_name": {"engineering"}}
	req := httptest.NewRequest("POST", "/web/template/coding/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("HX-Redirect"); got != "/config/templates/engineering" {
		t.Errorf("HX-Redirect = %q, want /config/templates/engineering", got)
	}

	cfg, err := config.Load(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatalf("reloading config: %v", err)
	}
	if _, ok := cfg.Templates["coding"]; ok {
		t.Error("old template key 'coding' still present")
	}
	if _, ok := cfg.Templates["engineering"]; !ok {
		t.Error("new template key 'engineering' missing")
	}

	records, err := agentstore.Load(agentstore.FilePath(dir))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	if got := records["leo-coding-leo"].Template; got != "engineering" {
		t.Errorf("agent record Template = %q, want engineering", got)
	}
}

func TestTemplateRenameCollision(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	form := url.Values{"new_name": {"research"}} // already exists
	req := httptest.NewRequest("POST", "/web/template/coding/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if got := w.Header().Get("HX-Redirect"); got != "" {
		t.Errorf("expected no HX-Redirect on collision, got %q", got)
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("expected collision flash, got %q", w.Body.String())
	}
}

func TestTemplateRenameInvalidName(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	form := url.Values{"new_name": {"bad name"}} // space is invalid
	req := httptest.NewRequest("POST", "/web/template/coding/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), entityNameError) {
		t.Errorf("expected name-format flash, got %q", w.Body.String())
	}
}
```

Add `"github.com/blackpaw-studio/leo/internal/agentstore"` to the test file's import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestTemplateRename ./internal/web/`
Expected: FAIL — route unregistered (404, so assertions miss) / `handleTemplateRename` undefined.

- [ ] **Step 3: Register the route**

In `internal/web/web.go`, next to the existing template routes (the `POST /web/template/add` / `DELETE /web/template/{name}` lines), add:

```go
	mux.HandleFunc("POST /web/template/{name}/rename", s.handleTemplateRename)
```

- [ ] **Step 4: Implement the handler**

In `internal/web/handlers.go`, add `"github.com/blackpaw-studio/leo/internal/agentstore"` to the import block, then add after `handleTemplateDelete`:

```go
// handleTemplateRename re-keys a template and cascades the new name to every
// task that referenced it (via config.RenameTemplate) and to persisted agent
// records' template pointers. Running/suspended agents keep their spawn-time
// identity (name + tmux session); only the Record.Template pointer moves.
//
// On success it HX-Redirects to the renamed template's edit page — the current
// URL still holds the old name. Error paths mirror handleWebAgentRename: the
// rename form targets a non-#flash-container element, so failures are retargeted
// to the shared flash container via renderFlashToContainer.
func (s *Server) handleTemplateRename(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	newName := r.FormValue("new_name")
	if newName == "" {
		s.renderFlashToContainer(w, "error", "New name is required")
		return
	}
	if newName == name {
		// No-op rename: bounce back to the same edit page.
		w.Header().Set("HX-Redirect", "/config/templates/"+url.PathEscape(name))
		w.WriteHeader(http.StatusOK)
		return
	}
	if !validEntityName(newName) {
		s.renderFlashToContainer(w, "error", entityNameError)
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlashToContainer(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if err := config.RenameTemplate(cfg, name, newName); err != nil {
		s.renderFlashToContainer(w, "error", err.Error())
		return
	}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlashToContainer(w, "error", errMsg)
		return
	}

	// Best-effort: move the template pointer on every persisted agent record
	// spawned from the old name. A failure here must not fail a rename that
	// already updated config + tasks, so we log and continue.
	if records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath)); err == nil {
		for recName, rec := range records {
			if rec.Template != name {
				continue
			}
			if err := agentstore.Update(cfg.HomePath, recName, func(r agentstore.Record) agentstore.Record {
				r.Template = newName
				return r
			}); err != nil {
				log.Printf("template rename %q→%q: agentstore.Update(%q) failed: %v", name, newName, recName, err)
			}
		}
	}

	s.reloadConfigOrWarn()

	w.Header().Set("HX-Redirect", "/config/templates/"+url.PathEscape(newName))
	w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race -run TestTemplateRename ./internal/web/`
Expected: PASS (all three).

- [ ] **Step 6: Run the package suites to catch regressions**

Run: `go test -race ./internal/web/ ./internal/config/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/web/handlers.go internal/web/web.go internal/web/handlers_templates_test.go
git commit -m "feat(web): add template rename handler with task + agentstore cascade"
```

---

### Task 3: Rename form on the template edit page

**Files:**
- Modify: `internal/web/templates/pages/template_edit.html`
- Test: `internal/web/handlers_templates_test.go`

**Interfaces:**
- Consumes: `.Data.Name` (from `templateEditData` in `internal/web/handlers_pages.go:405`); the route `POST /web/template/{name}/rename` (Task 2).
- Produces: rendered rename form on the edit page.

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_templates_test.go`:

```go
func TestTemplateEditShowsRenameForm(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("GET", "/config/templates/coding", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `hx-post="/web/template/coding/rename"`) {
		t.Errorf("expected rename form posting to /web/template/coding/rename, got %q", body)
	}
	if !strings.Contains(body, `name="new_name"`) {
		t.Error("expected a new_name input in the rename form")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestTemplateEditShowsRenameForm ./internal/web/`
Expected: FAIL — the edit page has no rename form yet.

- [ ] **Step 3: Add the rename form to the edit page**

Replace the contents of `internal/web/templates/pages/template_edit.html` with:

```html
{{define "page_template_edit"}}
<h1 class="page-title"><a href="/config/templates">Templates</a> / {{.Data.Name}}</h1>
<div class="card">
  <h2 class="card-title">Rename</h2>
  <form hx-post="/web/template/{{.Data.Name}}/rename" hx-target="#flash-container" hx-swap="innerHTML" class="inline-form">
    <input type="text" name="new_name" value="{{.Data.Name}}" required
           pattern="[A-Za-z0-9._-]+" aria-label="New template name" />
    <button type="submit">Rename</button>
  </form>
</div>
<div class="card">{{template "config_form" .Data.Form}}</div>
{{end}}
```

Note: `hx-target`/`hx-swap` are the success-path defaults; on success the handler sends `HX-Redirect`, which htmx honors regardless of target. On error the handler's `renderFlashToContainer` retargets to `#flash-container` itself, so the flash lands correctly either way.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race -run TestTemplateEditShowsRenameForm ./internal/web/`
Expected: PASS.

- [ ] **Step 5: Build and confirm nothing else broke**

Run: `make build && go test -race ./internal/web/`
Expected: build succeeds; web suite PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/web/templates/pages/template_edit.html internal/web/handlers_templates_test.go
git commit -m "feat(web): add rename form to template edit page"
```

---

### Task 4: Full verification

- [ ] **Step 1: Run the full test suite**

Run: `make test`
Expected: PASS across all packages.

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: clean (go vet + staticcheck).

- [ ] **Step 3: Manual smoke (optional, if a dev daemon is available)**

Open the web UI → Templates → pick a template → edit page → Rename card. Rename it; confirm the URL becomes `/config/templates/<newName>`, the template list shows the new name, and any task that referenced it now points at the new name in `leo.yaml`.

---

## Self-Review Notes

- **Spec coverage:** §1 config helper → Task 1; §2 agentstore cascade → Task 2 (best-effort loop); §3 handler + route → Task 2; §4 UI → Task 3; §Testing → Tasks 1–3 tests + Task 4 full run. All covered.
- **Layering:** `config.RenameTemplate` takes no dependency on `web`; name-shape validation lives in the handler (Task 2 Step 4) — matches the Global Constraint.
- **Type consistency:** `RenameTemplate(cfg *Config, oldName, newName string) error` is defined in Task 1 and called with that exact signature in Task 2. `agentstore.Update(homePath, name string, mutate func(Record) Record) error` and `agentstore.Load(path string)` match `internal/agentstore/store.go`.
- **No task-ref cascade in the handler test:** deliberate — the shared `testConfigWithTemplatesYAML` has no task targeting a template, and task-ref rewriting is already covered by Task 1's unit test. The handler test covers re-key + HX-Redirect + agentstore cascade.
