package web

import "github.com/blackpaw-studio/leo/internal/config"

// lockConfigWrite takes the config write lock and returns its release, for
// handlers that load, mutate, and save leo.yaml:
//
//	defer s.lockConfigWrite()()
func (s *Server) lockConfigWrite() func() {
	return s.configWriter.Lock()
}

// mutateConfig loads config, applies apply, validates, saves, and reloads,
// all under the config write lock so saves and reloads stay ordered. A
// non-empty errMsg means nothing was written; warn reports a failed reload.
func (s *Server) mutateConfig(apply func(*config.Config) error) (warn, errMsg string) {
	defer s.lockConfigWrite()()
	cfg, err := s.loadConfig()
	if err != nil {
		return "", err.Error()
	}
	if err := apply(cfg); err != nil {
		return "", err.Error()
	}
	if msg := s.validateAndSave(cfg); msg != "" {
		return "", msg
	}
	return s.reloadConfigOrWarn(), ""
}
