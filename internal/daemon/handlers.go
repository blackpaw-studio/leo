package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
)

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
