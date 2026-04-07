package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
)

func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	var req TaskAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding request: %v", err))
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
		Schedule:   req.Schedule,
		PromptFile: req.PromptFile,
		Model:      req.Model,
		MaxTurns:   req.MaxTurns,
		TopicID:    req.TopicID,
		Silent:     req.Silent,
		Enabled:    req.Enabled,
	}

	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("saving config: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleTaskRemove(w http.ResponseWriter, r *http.Request) {
	var req TaskNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding request: %v", err))
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding request: %v", err))
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

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	data, err := json.Marshal(cfg.Tasks)
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

	leoPath, err := exec.LookPath("leo")
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("finding leo binary: %v", err))
		return
	}

	if err := cron.Install(cfg, leoPath); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("installing cron: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleCronRemove(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if err := cron.Remove(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("removing cron: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleCronList(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	block, err := cron.List(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("listing cron: %v", err))
		return
	}

	data, _ := json.Marshal(map[string]string{"entries": block})
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}
