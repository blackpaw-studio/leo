package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/observe"
)

const (
	defaultHistoryLimit = 20
	maxHistoryLimit     = 200
	// maxTaskLogBytes bounds one run-log response; a longer log is read from
	// its tail only and reported truncated.
	maxTaskLogBytes = 4 << 20

	codeUnavailable = "unavailable"
	codeInternal    = "internal"
)

// launchTask starts `leo run <name>` detached; the run reports through task
// history, not through this call.
func (c *ConfigService) launchTask(name string) error {
	cmd := c.execCommand(c.leoPath, "run", name, "--config", c.configPath)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck // runs independently
	return nil
}

// launchServiceRestart starts a graceful, unprompted `leo service restart`
// and drops the service-restart banner. Busy agents are always drained first
// and nobody is asked a question: --graceful skips the busy prompt, --force
// the timeout one.
func (c *ConfigService) launchServiceRestart() error {
	// Single-flight: a restart drains agents for up to minutes, so a second
	// request while one is running is refused, not queued behind it.
	if !c.restartInFlight.CompareAndSwap(false, true) {
		return errRestartInProgress()
	}
	// Released by defer so a panic anywhere below cannot leave restart stuck
	// at 409; once the process is running its watcher goroutine owns the flag.
	handedOff := false
	defer func() {
		if !handedOff {
			c.restartInFlight.Store(false)
		}
	}()
	cmd := c.execCommand(c.leoPath, "service", "restart", "--graceful", "--force", "--config", c.configPath)
	if err := cmd.Start(); err != nil {
		return err
	}
	handedOff = true
	go func() {
		defer c.restartInFlight.Store(false)
		_ = cmd.Wait() // on success this daemon is replaced before it returns
	}()
	c.serviceRestartNeeded.Store(false)
	return nil
}

// loadForRead loads the config for a read-only route.
func (c *ConfigService) loadForRead() (*config.Config, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, configUnavailable(err)
	}
	return cfg, nil
}

// taskOf loads the config and resolves the {name} task, or the 404/503.
func (c *ConfigService) taskOf(r *http.Request) (*config.Config, string, config.TaskConfig, error) {
	cfg, err := c.loadForRead()
	if err != nil {
		return nil, "", config.TaskConfig{}, err
	}
	name := r.PathValue("name")
	task, ok := cfg.Tasks[name]
	if !ok {
		return nil, "", config.TaskConfig{}, errNotFound("task %q not found", name)
	}
	return cfg, name, task, nil
}

func (c *ConfigService) registerOpsRoutes(route func(string, http.HandlerFunc)) {
	route("POST /tasks/{name}/run", c.handleTaskRunAPI)
	route("GET /tasks/{name}/history", c.handleTaskHistoryAPI)
	route("GET /tasks/{name}/log", c.handleTaskLogAPI)
	route("GET /config/tasks/{name}/prompt", c.handlePromptGetAPI)
	route("PUT /config/tasks/{name}/prompt", c.handlePromptPutAPI)
	route("GET /cron/preview", c.handleCronPreviewAPI)
	route("POST /service/restart", c.handleServiceRestartAPI)
	route("POST /service/restart-agents", c.handleRestartAgentsAPI)
	route("GET /service/log", c.handleServiceLogAPI)
	route("GET /service/status", c.handleServiceStatusAPI)
}

func (c *ConfigService) handleTaskRunAPI(w http.ResponseWriter, r *http.Request) {
	_, name, _, err := c.taskOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	if err := c.launchTask(name); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("starting task: %v", err), Code: codeInternal})
		return
	}
	writeConfigData(w, http.StatusAccepted, map[string]string{"name": name, "status": "started"})
}

func (c *ConfigService) handleTaskHistoryAPI(w http.ResponseWriter, r *http.Request) {
	cfg, name, _, err := c.taskOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	limit := defaultHistoryLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n < 1 {
			writeConfigError(w, errBadRequest("limit must be a positive integer"))
			return
		}
		limit = min(n, maxHistoryLimit)
	}
	entries := c.loadHistory(cfg).GetAll(name) // already newest first
	if len(entries) > limit {
		entries = entries[:limit]
	}
	if entries == nil {
		entries = []history.Entry{}
	}
	writeConfigData(w, http.StatusOK, entries)
}

type taskLogDoc struct {
	File      string `json:"file"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (c *ConfigService) handleTaskLogAPI(w http.ResponseWriter, r *http.Request) {
	cfg, name, _, err := c.taskOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	file := r.URL.Query().Get("file")
	if file == "" {
		writeConfigError(w, errBadRequest("missing file parameter"))
		return
	}
	// Only the basename is honored, so a path cannot leave the logs dir.
	file = filepath.Base(file)
	if !strings.HasPrefix(file, name+"-") {
		writeConfigError(w, errBadRequest("log file does not match task"))
		return
	}
	text, truncated, err := readLogFileTail(filepath.Join(cfg.StatePath(), "logs", file), c.logLimit())
	if errors.Is(err, fs.ErrNotExist) {
		writeConfigError(w, errNotFound("log file %q not found", file))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("reading log file: %v", err), Code: codeInternal})
		return
	}
	writeConfigData(w, http.StatusOK, taskLogDoc{File: file, Text: text, Truncated: truncated})
}

func (c *ConfigService) logLimit() int64 {
	if c.maxLogBytes > 0 {
		return c.maxLogBytes
	}
	return maxTaskLogBytes
}

// readLogFileTail reads at most limit bytes from the end of path, never the
// whole file. When it had to cut, the partial first line is dropped so the text
// starts on a line boundary.
func readLogFileTail(path string, limit int64) (text string, truncated bool, err error) {
	f, err := os.Open(path) //nolint:gosec // basename of a task-prefixed file under the state logs dir
	if err != nil {
		return "", false, err
	}
	defer f.Close() //nolint:errcheck
	info, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	start := int64(0)
	if info.Size() > limit {
		// One byte more than the limit, to tell whether the cut fell on a line start.
		start, truncated = info.Size()-limit-1, true
	}
	buf := make([]byte, info.Size()-start)
	if _, err := io.ReadFull(io.NewSectionReader(f, start, int64(len(buf))), buf); err != nil {
		return "", false, err
	}
	if truncated {
		if buf[0] == '\n' {
			buf = buf[1:]
		} else if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	return string(buf), truncated, nil
}

type promptDoc struct {
	Revision string   `json:"revision"`
	File     string   `json:"file"`
	Files    []string `json:"files"`
	Content  string   `json:"content"`
}

// readPromptBytes reads a prompt file's raw bytes; a missing file is empty
// (the create-on-first-write case).
func (c *ConfigService) readPromptBytes(cfg *config.Config, workspace, file string) ([]byte, error) {
	if _, err := c.resolveGuardedPrompt(cfg, workspace, file); err != nil {
		return nil, err
	}
	data, err := config.ReadPromptBytes(workspace, file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading prompt file: %w", err)
	}
	return data, nil
}

func (c *ConfigService) handlePromptGetAPI(w http.ResponseWriter, r *http.Request) {
	cfg, _, task, err := c.taskOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	// Only the task's configured prompt_file is served; a ?file= query is
	// ignored (the app switches files by editing the prompt_file field).
	file := task.PromptFile
	workspace := cfg.TaskWorkspace(task)
	files := c.listPromptFiles(cfg, workspace)
	doc := promptDoc{File: file, Files: files, Revision: config.RevisionOf(nil)}
	if file != "" {
		data, err := c.readPromptBytes(cfg, workspace, file)
		if err != nil {
			writeConfigError(w, asConfigError(err))
			return
		}
		doc.Content, doc.Revision = string(data), config.RevisionOf(data)
	}
	w.Header().Set("ETag", `"`+doc.Revision+`"`)
	writeConfigData(w, http.StatusOK, doc)
}

// listPromptFiles lists the workspace's markdown files, leaving out leo's own
// state files.
func (c *ConfigService) listPromptFiles(cfg *config.Config, workspace string) []string {
	all, _ := config.ListPromptFiles(workspace)
	files := make([]string, 0, len(all))
	for _, f := range all {
		if abs, err := config.ResolvePromptPath(workspace, f); err == nil && !c.protectedPrompt(cfg, abs) {
			files = append(files, f)
		}
	}
	return files
}

func (c *ConfigService) handlePromptPutAPI(w http.ResponseWriter, r *http.Request) {
	ifMatch, err := ifMatchOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	body, ok := delegationBody[struct {
		Content *string `json:"content"`
	}](w, r)
	if !ok {
		return
	}
	if body.Content == nil {
		writeConfigError(w, errBadRequest(`body must be {"content": "..."}`))
		return
	}
	defer c.lockConfigWrite()()
	cfg, _, task, err := c.taskOf(r)
	if err != nil {
		writeConfigError(w, err)
		return
	}
	if task.PromptFile == "" {
		writeConfigError(w, errInvalid("", "task has no prompt_file"))
		return
	}
	workspace := cfg.TaskWorkspace(task)
	current, err := c.readPromptBytes(cfg, workspace, task.PromptFile)
	if err != nil {
		writeConfigError(w, asConfigError(err))
		return
	}
	if rev := config.RevisionOf(current); rev != ifMatch {
		writeConfigError(w, errConflict(rev))
		return
	}
	if err := config.WritePromptFile(workspace, task.PromptFile, *body.Content); err != nil {
		writeConfigError(w, errInvalid("", fmt.Sprintf("Failed to save prompt: %v", err)))
		return
	}
	rev := config.RevisionOf([]byte(*body.Content))
	w.Header().Set("ETag", `"`+rev+`"`)
	writeConfigData(w, http.StatusOK, map[string]string{"revision": rev, "file": task.PromptFile})
}

type cronPreviewDoc struct {
	Valid       bool   `json:"valid"`
	Description string `json:"description"`
	Error       string `json:"error,omitempty"`
}

func (c *ConfigService) handleCronPreviewAPI(w http.ResponseWriter, r *http.Request) {
	expr := r.URL.Query().Get("expr")
	if expr == "" {
		writeConfigError(w, errBadRequest("missing expr parameter"))
		return
	}
	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	if _, err := parser.Parse(expr); err != nil {
		writeConfigData(w, http.StatusOK, cronPreviewDoc{Error: err.Error()})
		return
	}
	writeConfigData(w, http.StatusOK, cronPreviewDoc{Valid: true, Description: describeCron(expr)})
}

func (c *ConfigService) handleServiceRestartAPI(w http.ResponseWriter, _ *http.Request) {
	if err := c.launchServiceRestart(); err != nil {
		var ce *ConfigError
		if errors.As(err, &ce) {
			writeConfigError(w, ce)
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("restarting service: %v", err), Code: codeInternal})
		return
	}
	writeConfigData(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

type restartAgentsDoc struct {
	Restarted []string          `json:"restarted"`
	Skipped   []string          `json:"skipped"`
	Failed    map[string]string `json:"failed"`
}

func (c *ConfigService) handleRestartAgentsAPI(w http.ResponseWriter, _ *http.Request) {
	var restarter AgentRestarter
	if c.agents != nil {
		restarter = c.agents()
	}
	if restarter == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent service not available", Code: codeUnavailable})
		return
	}
	result := restarter.RestartAll()
	doc := restartAgentsDoc{Restarted: orEmpty(result.Restarted), Skipped: orEmpty(result.Skipped), Failed: map[string]string{}}
	for name, err := range result.Failed {
		doc.Failed[name] = err.Error()
	}
	if len(doc.Failed) == 0 {
		c.ClearAgentsRestartNeeded()
	}
	writeConfigData(w, http.StatusOK, doc)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (c *ConfigService) handleServiceLogAPI(w http.ResponseWriter, r *http.Request) {
	n := serviceLogTailDefaultLines
	if raw := r.URL.Query().Get("lines"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeConfigError(w, errBadRequest("lines must be a positive integer"))
			return
		}
		n = min(parsed, serviceLogTailMaxLines)
	}
	path := c.logFilePath()
	if path == "" {
		writeConfigError(w, errNotFound("log path not configured"))
		return
	}
	text, err := readLogTail(path, n)
	if errors.Is(err, fs.ErrNotExist) {
		text, err = "", nil
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("reading service log: %v", err), Code: codeInternal})
		return
	}
	writeConfigData(w, http.StatusOK, map[string]string{"text": text})
}

type serviceStatusDoc struct {
	Version         string             `json:"version"`
	PID             int                `json:"pid"`
	StartedAt       time.Time          `json:"started_at"`
	ConfigPath      string             `json:"config_path"`
	LogPath         string             `json:"log_path"`
	RestartRequired RestartRequired    `json:"restart_required"`
	Processes       []ProcessStateInfo `json:"processes"`
}

func (c *ConfigService) handleServiceStatusAPI(w http.ResponseWriter, _ *http.Request) {
	doc := serviceStatusDoc{
		PID: c.pid, StartedAt: c.startedAt, ConfigPath: c.configPath, LogPath: c.logFilePath(),
		RestartRequired: c.RestartFlags(), Processes: []ProcessStateInfo{},
	}
	if c.version != nil {
		doc.Version = c.version()
	}
	if c.processes != nil {
		if p := c.processes(); p != nil {
			for _, st := range p.States() {
				doc.Processes = append(doc.Processes, st)
			}
			sort.Slice(doc.Processes, func(i, j int) bool { return doc.Processes[i].Name < doc.Processes[j].Name })
		}
	}
	writeConfigData(w, http.StatusOK, doc)
}

type reloadDoc struct {
	Revision string `json:"revision"`
}

// handleConfigReloadAPI re-reads leo.yaml into the running daemon.
func (c *ConfigService) handleConfigReloadAPI(w http.ResponseWriter, _ *http.Request) {
	if c.reloader != nil {
		if err := c.reloader.ReloadConfig(); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("reload failed: %v", err), Code: codeInternal})
			return
		}
	}
	_, rev, err := c.loadLocked()
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeConfigData(w, http.StatusOK, reloadDoc{Revision: rev})
}

type versionDoc struct {
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// handleAPIVersion is GET /api/v1/version: the build version and the optional
// API features this daemon offers, the web twin of the socket's GET /version.
func (s *Server) handleAPIVersion(w http.ResponseWriter, _ *http.Request) {
	writeConfigData(w, http.StatusOK, versionDoc{Version: s.version, Capabilities: observe.Features()})
}
