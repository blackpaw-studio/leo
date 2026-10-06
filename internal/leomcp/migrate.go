package leomcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
)

// openCodeConfigEnv carries opencode's per-spawn config overlay.
const openCodeConfigEnv = "OPENCODE_CONFIG_CONTENT"

// MigrateLaunch points the leo MCP server in a persisted launch (an agent
// record's argv and env) at s's binary, so a launch recorded while the MCP
// server was a bare "leo" from PATH — or a different leo's path — is
// replayed with the daemon's own binary. Only the leo server's command is
// rewritten; every other argv element and env entry is returned
// byte-identical, and the inputs are never mutated.
//
//   - claude ("" or "claude"): an inline --mcp-config JSON value, and the
//     leo-managed leo-mcp.json file a --mcp-config names, which is rewritten
//     on disk.
//   - codex: the exact `mcp_servers.leo.command=...` -c value.
//   - opencode: the leo server's command in OPENCODE_CONFIG_CONTENT.
//
// The zero Server is a no-op: with no resolved binary there is nothing
// better to point at.
//
// It runs in two phases: every change (argv, env, and the managed file's
// new bytes) is computed and validated first, and the managed file is
// written only once all of it succeeded. On error (a malformed leo entry, or
// a failed write) it returns copies of the launch exactly as given and the
// file is left as it was, so the caller can log the error and launch
// unmigrated.
//
// A record whose managed config lives under a different leo home (its
// --mcp-config is not this home's ConfigPath) is deliberately not
// migrated; re-spawning that agent fixes it.
func (s Server) MigrateLaunch(cfg *config.Config, harnessName string, args []string, env map[string]string) ([]string, map[string]string, error) {
	unmigrated := func(err error) ([]string, map[string]string, error) {
		return append([]string(nil), args...), copyEnv(env), err
	}
	migratedArgs, migratedEnv, write, err := s.planLaunch(cfg, harnessName, args, env)
	if err != nil {
		return unmigrated(err)
	}
	if write != nil {
		if err := writeFileAtomic(write.path, write.data, write.perm); err != nil {
			return unmigrated(fmt.Errorf("write %s: %w", write.path, err))
		}
	}
	return migratedArgs, migratedEnv, nil
}

// fileWrite is a managed-file rewrite planned by planLaunch.
type fileWrite struct {
	path string
	data []byte
	perm os.FileMode
}

// planLaunch computes the migrated launch and the managed-file rewrite, if
// any, without writing anything.
func (s Server) planLaunch(cfg *config.Config, harnessName string, args []string, env map[string]string) ([]string, map[string]string, *fileWrite, error) {
	outArgs := append([]string(nil), args...)
	outEnv := copyEnv(env)
	if s.Bin == "" {
		return outArgs, outEnv, nil, nil
	}
	switch harnessName {
	case "", "claude":
		managed := ""
		if cfg != nil {
			managed = ConfigPath(cfg)
		}
		write, err := s.planClaudeArgs(outArgs, managed)
		return outArgs, outEnv, write, err
	case "codex":
		for i, a := range outArgs {
			if strings.HasPrefix(a, codexharness.LeoMCPCommandKey) {
				outArgs[i] = codexharness.LeoMCPCommandArg(s.Bin)
			}
		}
	case "opencode":
		content, ok := outEnv[openCodeConfigEnv]
		if !ok {
			break
		}
		migrated, err := setLeoServer([]byte(content), "mcp", s.setCommandArray)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("migrate %s: %w", openCodeConfigEnv, err)
		}
		outEnv[openCodeConfigEnv] = string(migrated)
	}
	return outArgs, outEnv, nil, nil
}

// planClaudeArgs rewrites, in place, every inline --mcp-config value and
// plans the rewrite of the managed config file (managedPath, the file
// EnsureConfig writes) when a --mcp-config names it. A user's own file is
// never touched, even one named leo-mcp.json.
func (s Server) planClaudeArgs(args []string, managedPath string) (*fileWrite, error) {
	var write *fileWrite
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--mcp-config" {
			continue
		}
		value := args[i+1]
		switch {
		case strings.HasPrefix(strings.TrimSpace(value), "{"):
			migrated, err := setLeoServer([]byte(value), "mcpServers", s.setCommandString)
			if err != nil {
				return nil, fmt.Errorf("migrate inline --mcp-config: %w", err)
			}
			args[i+1] = string(migrated)
		case managedPath != "" && filepath.Clean(value) == filepath.Clean(managedPath):
			planned, err := s.planClaudeConfigFile(value)
			if err != nil {
				return nil, err
			}
			if planned != nil {
				write = planned
			}
		}
	}
	return write, nil
}

// planClaudeConfigFile returns the managed file's migrated bytes, keeping
// its mode, or nil when it is already current.
func (s Server) planClaudeConfigFile(path string) (*fileWrite, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Gone (e.g. a wiped state dir): write it fresh.
		return &fileWrite{path: path, data: s.buildConfig(), perm: 0o644}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	migrated, err := setLeoServer(raw, "mcpServers", s.setCommandString)
	if err != nil {
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	if bytes.Equal(migrated, raw) {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	return &fileWrite{path: path, data: migrated, perm: info.Mode().Perm()}, nil
}

// setLeoServer decodes doc, applies set to doc[serversKey]["leo"], and
// re-encodes it. Every other value is carried as raw JSON so it survives
// unchanged. A doc with no leo server is returned as is; a level that is
// null or not an object is an error.
func setLeoServer(doc []byte, serversKey string, set func(map[string]json.RawMessage) error) ([]byte, error) {
	top, err := jsonObject(doc, "config")
	if err != nil {
		return nil, err
	}
	rawServers, ok := top[serversKey]
	if !ok {
		return doc, nil
	}
	servers, err := jsonObject(rawServers, serversKey)
	if err != nil {
		return nil, err
	}
	rawLeo, ok := servers["leo"]
	if !ok {
		return doc, nil
	}
	leo, err := jsonObject(rawLeo, serversKey+".leo")
	if err != nil {
		return nil, err
	}
	if err := set(leo); err != nil {
		return nil, err
	}
	if servers["leo"], err = json.Marshal(leo); err != nil {
		return nil, err
	}
	if top[serversKey], err = json.Marshal(servers); err != nil {
		return nil, err
	}
	out, err := json.Marshal(top)
	if err != nil {
		return nil, err
	}
	if bytes.HasSuffix(doc, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, nil
}

// jsonObject decodes raw as a JSON object, rejecting null and every
// non-object value (json.Unmarshal would turn null into a nil map).
func jsonObject(raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%s is not a JSON object", what)
	}
	return obj, nil
}

// setCommandString sets a claude MCP entry's command to s.Bin.
func (s Server) setCommandString(leo map[string]json.RawMessage) error {
	raw, err := json.Marshal(s.Bin)
	leo["command"] = raw
	return err
}

// setCommandArray replaces the binary (first element) of an opencode MCP
// entry's command, keeping its arguments.
func (s Server) setCommandArray(leo map[string]json.RawMessage) error {
	var command []string
	if err := json.Unmarshal(leo["command"], &command); err != nil || len(command) == 0 {
		return fmt.Errorf("leo server command is not a non-empty string array")
	}
	command[0] = s.Bin
	raw, err := json.Marshal(command)
	leo["command"] = raw
	return err
}

func copyEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}
