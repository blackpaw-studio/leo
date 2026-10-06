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
// better to point at. On error (a malformed leo entry) it returns copies of
// the launch exactly as given, never a partial migration, so the caller can
// log the error and launch unmigrated.
func (s Server) MigrateLaunch(cfg *config.Config, harnessName string, args []string, env map[string]string) ([]string, map[string]string, error) {
	migratedArgs, migratedEnv, err := s.migrateLaunch(cfg, harnessName, args, env)
	if err != nil {
		return append([]string(nil), args...), copyEnv(env), err
	}
	return migratedArgs, migratedEnv, nil
}

func (s Server) migrateLaunch(cfg *config.Config, harnessName string, args []string, env map[string]string) ([]string, map[string]string, error) {
	outArgs := append([]string(nil), args...)
	outEnv := copyEnv(env)
	if s.Bin == "" {
		return outArgs, outEnv, nil
	}
	switch harnessName {
	case "", "claude":
		managed := ""
		if cfg != nil {
			managed = ConfigPath(cfg)
		}
		return outArgs, outEnv, s.migrateClaudeArgs(outArgs, managed)
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
		migrated, err := setLeoServer([]byte(content), "mcp", func(leo map[string]json.RawMessage) error {
			return s.setCommandArray(leo)
		})
		if err != nil {
			return outArgs, outEnv, fmt.Errorf("migrate %s: %w", openCodeConfigEnv, err)
		}
		outEnv[openCodeConfigEnv] = string(migrated)
	}
	return outArgs, outEnv, nil
}

// migrateClaudeArgs rewrites, in place, every inline --mcp-config value and
// the managed config file (managedPath, the file EnsureConfig writes) when a
// --mcp-config names it. A user's own file is never touched, even one named
// leo-mcp.json.
func (s Server) migrateClaudeArgs(args []string, managedPath string) error {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--mcp-config" {
			continue
		}
		value := args[i+1]
		switch {
		case strings.HasPrefix(strings.TrimSpace(value), "{"):
			migrated, err := setLeoServer([]byte(value), "mcpServers", s.setCommandString)
			if err != nil {
				return fmt.Errorf("migrate inline --mcp-config: %w", err)
			}
			args[i+1] = string(migrated)
		case managedPath != "" && filepath.Clean(value) == filepath.Clean(managedPath):
			if err := s.migrateClaudeConfigFile(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s Server) migrateClaudeConfigFile(path string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Gone (e.g. a wiped state dir): write it fresh.
		return writeFileAtomic(path, s.buildConfig(), 0o644)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	migrated, err := setLeoServer(raw, "mcpServers", s.setCommandString)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", path, err)
	}
	if bytes.Equal(migrated, raw) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := writeFileAtomic(path, migrated, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
