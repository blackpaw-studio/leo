package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/redact"
)

// decodeJSON decodes a JSON request body into v and writes an error response on failure.
// Returns true if decoding succeeded, false if an error was written.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding request: %v", err))
		return false
	}
	return true
}

func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	var req TaskAddRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Schedule == "" {
		writeError(w, http.StatusBadRequest, "schedule is required")
		return
	}
	if req.PromptFile == "" {
		writeError(w, http.StatusBadRequest, "prompt_file is required")
		return
	}

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if cfg.Tasks == nil {
		cfg.Tasks = make(map[string]config.TaskConfig)
	}

	cfg.Tasks[req.Name] = config.TaskConfig{
		Schedule:     req.Schedule,
		PromptFile:   req.PromptFile,
		Model:        req.Model,
		MaxTurns:     req.MaxTurns,
		Channels:     req.Channels,
		NotifyOnFail: req.NotifyOnFail,
		Silent:       req.Silent,
		Enabled:      req.Enabled,
	}

	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("saving config: %v", err))
		return
	}

	s.syncScheduler(cfg)
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleTaskRemove(w http.ResponseWriter, r *http.Request) {
	var req TaskNameRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if _, ok := cfg.Tasks[req.Name]; !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("task %q not found", req.Name))
		return
	}

	delete(cfg.Tasks, req.Name)

	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("saving config: %v", err))
		return
	}

	s.syncScheduler(cfg)
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleTaskEnable(w http.ResponseWriter, r *http.Request) {
	s.setTaskEnabled(w, r, true)
}

func (s *Server) handleTaskDisable(w http.ResponseWriter, r *http.Request) {
	s.setTaskEnabled(w, r, false)
}

func (s *Server) setTaskEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	var req TaskNameRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	task, ok := cfg.Tasks[req.Name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("task %q not found", req.Name))
		return
	}

	task.Enabled = enabled
	cfg.Tasks[req.Name] = task

	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("saving config: %v", err))
		return
	}

	s.syncScheduler(cfg)
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// taskListInfo is the public view of a task on GET /task/list.
//
// It carries EnvKeys rather than the env map: this endpoint is served on the
// daemon's Unix socket, which any agent can reach with a one-line curl, and
// task env holds credentials. Key names describe what a task configures
// without disclosing any of it. (The web UI's /api/task/list projects the
// same way.)
type taskListInfo struct {
	Name       string   `json:"name"`
	Schedule   string   `json:"schedule,omitempty"`
	Timezone   string   `json:"timezone,omitempty"`
	PromptFile string   `json:"prompt_file,omitempty"`
	Model      string   `json:"model,omitempty"`
	Harness    string   `json:"harness,omitempty"`
	Workspace  string   `json:"workspace,omitempty"`
	Runtime    string   `json:"runtime,omitempty"`
	Template   string   `json:"template,omitempty"`
	Enabled    bool     `json:"enabled"`
	Channels   []string `json:"channels,omitempty"`
	EnvKeys    []string `json:"env_keys,omitempty"`
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	tasks := make([]taskListInfo, 0, len(cfg.Tasks))
	for name, task := range cfg.Tasks {
		tasks = append(tasks, taskListInfo{
			Name:       name,
			Schedule:   task.Schedule,
			Timezone:   task.Timezone,
			PromptFile: task.PromptFile,
			Model:      task.Model,
			Harness:    task.Harness,
			Workspace:  task.Workspace,
			Runtime:    task.Runtime,
			Template:   task.Template,
			Enabled:    task.Enabled,
			Channels:   task.Channels,
			EnvKeys:    redact.Keys(task.Env),
		})
	}
	// Config maps iterate in random order; sort for a stable listing.
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })

	data, err := json.Marshal(tasks)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling tasks: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

func (s *Server) handleCronInstall(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if err := s.scheduler.Install(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("installing schedules: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleCronRemove(w http.ResponseWriter, r *http.Request) {
	s.scheduler.Remove()
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleCronList(w http.ResponseWriter, r *http.Request) {
	entries := s.scheduler.List()
	data, err := json.Marshal(entries)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling cron entries: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if err := s.scheduler.Install(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("reloading schedules: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

// syncScheduler re-syncs the in-process scheduler with the current config.
// Errors are logged but not returned because the on-disk config write already succeeded.
func (s *Server) syncScheduler(cfg *config.Config) {
	if err := s.scheduler.Install(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: scheduler sync failed: %v\n", err)
	}
}
