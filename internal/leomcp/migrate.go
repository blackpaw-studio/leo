package leomcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
)

// configFileName is the basename of the leo-managed claude MCP config
// (ConfigPath). Only a --mcp-config naming this file is rewritten; a
// user's own MCP config is never touched.
const configFileName = "leo-mcp.json"

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
func (s Server) MigrateLaunch(harnessName string, args []string, env map[string]string) ([]string, map[string]string, error) {
	outArgs := append([]string(nil), args...)
	outEnv := copyEnv(env)
	if s.Bin == "" {
		return outArgs, outEnv, nil
	}
	switch harnessName {
	case "", "claude":
		return outArgs, outEnv, s.migrateClaudeArgs(outArgs)
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
// the leo-managed config file a --mcp-config names.
func (s Server) migrateClaudeArgs(args []string) error {
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
		case filepath.Base(value) == configFileName:
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
	if err := writeFileAtomic(path, migrated, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// setLeoServer decodes doc, applies set to doc[serversKey]["leo"], and
// re-encodes it. Every other value is carried as raw JSON so it survives
// unchanged. A doc without a leo server is returned as is.
func setLeoServer(doc []byte, serversKey string, set func(map[string]json.RawMessage) error) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return nil, err
	}
	var servers map[string]json.RawMessage
	if raw, ok := top[serversKey]; !ok || json.Unmarshal(raw, &servers) != nil {
		return doc, nil
	}
	var leo map[string]json.RawMessage
	if raw, ok := servers["leo"]; !ok || json.Unmarshal(raw, &leo) != nil {
		return doc, nil
	}
	if err := set(leo); err != nil {
		return nil, err
	}
	var err error
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
