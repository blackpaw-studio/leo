package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agentmove"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// moveVerbFn is the daemon call behind `leo agent move-receive` (a test seam).
var moveVerbFn = daemon.MoveVerb

// maxManifestBytes bounds the manifest read from stdin.
const maxManifestBytes = 8 << 20

// newAgentMoveReceiveCmd is the destination end of `leo agent move` and
// `copy`: the source runs it over ssh with the manifest on stdin. It pipes
// stdin to this host's daemon and the answer to stdout; a refusal prints
// {error,message} and exits nonzero.
func newAgentMoveReceiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "move-receive <" + strings.Join(agentmove.Verbs, "|") + ">",
		Short:  "Destination side of agent move/copy (run by the source host over ssh)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Landing an agent creates one: an agent denied spawning may not.
			if err := gateCommand(cmd, "leo_spawn_agent"); err != nil {
				return err
			}
			verb := args[0]
			if !slices.Contains(agentmove.Verbs, verb) {
				return fmt.Errorf("unknown move verb %q", verb)
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			body, err := io.ReadAll(io.LimitReader(agentStdin, maxManifestBytes+1))
			if err != nil {
				return fmt.Errorf("reading the manifest: %w", err)
			}
			if len(body) > maxManifestBytes {
				return fmt.Errorf("manifest larger than %d bytes", maxManifestBytes)
			}
			status, out, err := moveVerbFn(cmd.Context(), cfg.HomePath, verb, body)
			if err != nil {
				out, _ = json.Marshal(agentmove.VerbError{Code: agentmove.CodeDaemonUnreachable, Message: err.Error()})
				fmt.Fprintln(agentStdout, string(out))
				return err
			}
			fmt.Fprintln(agentStdout, strings.TrimSpace(string(out)))
			if status != http.StatusOK {
				return fmt.Errorf("move-receive %s refused (HTTP %d)", verb, status)
			}
			return nil
		},
	}
}
