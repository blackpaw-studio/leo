package leomcp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Server identifies the leo binary every harness launches as its leo MCP
// server. The zero value launches the bare FallbackBin.
type Server struct {
	// Bin is the absolute leo executable to run; "" means FallbackBin.
	Bin string
}

// FallbackBin is the bare command run when the leo binary's own path is
// unknown. It resolves through the spawned agent's PATH, so it may be a
// different leo than the one that launched the agent.
const FallbackBin = "leo"

// serverArgs is the leo subcommand that serves MCP over stdio.
var serverArgs = []string{"mcp-server"}

// ResolveServer resolves the running leo binary once, from executable
// (os.Executable in production), so every agent runs the MCP server of the
// leo that launched it — an isolated test daemon's ./bin/leo, not whichever
// leo is first on the agent's PATH. When that fails it warns on warn and
// falls back to the bare FallbackBin.
//
// The path is deliberately NOT passed through filepath.EvalSymlinks, the
// same choice LEO_BRIDGE_BIN makes (service.wireBridge hands the bridge
// os.Executable as is). Agents outlive the binary that spawned them: after
// `brew upgrade` removes the old versioned Caskroom/Cellar directory, a
// resolved path would point at a deleted file for every agent launched
// before the upgrade, while the stable /opt/homebrew/bin/leo symlink keeps
// working and already names the new version.
func ResolveServer(executable func() (string, error), warn io.Writer) Server {
	bin, err := executable()
	if err == nil && !filepath.IsAbs(bin) {
		bin, err = filepath.Abs(bin)
	}
	if err == nil {
		_, err = os.Stat(bin)
	}
	if err != nil {
		fmt.Fprintf(warn, "leo: warning: cannot resolve the leo binary, agents will run %q from PATH as their MCP server: %v\n", FallbackBin, err)
		return Server{}
	}
	return Server{Bin: bin}
}

// Executable returns the leo binary the MCP server launches.
func (s Server) Executable() string {
	if s.Bin == "" {
		return FallbackBin
	}
	return s.Bin
}

// Args returns the arguments after Executable that start the MCP server.
func (s Server) Args() []string { return append([]string(nil), serverArgs...) }

// Command returns the full argv: Executable followed by Args.
func (s Server) Command() []string { return append([]string{s.Executable()}, serverArgs...) }
