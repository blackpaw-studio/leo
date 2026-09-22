package cli

import (
	"fmt"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/spf13/cobra"
)

func newDelegationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "delegation", Short: "Manage delegation profiles"}
	cmd.AddCommand(newDelegationListCmd(), newDelegationShowCmd(), newDelegationRenderCmd(), newDelegationResolveCmd(), newDelegationUseCmd())
	return cmd
}

func localDelegationConfig() (*config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	res, err := cfg.ResolveHost("")
	if err != nil {
		return nil, err
	}
	if !res.Localhost {
		return nil, fmt.Errorf("delegation commands are not supported in remote-client mode")
	}
	if cfg.Delegation == nil {
		return nil, fmt.Errorf("delegation not configured")
	}
	return cfg, nil
}

// delegationName rejects names the shared config validator would never accept,
// so "." and ".." fail with a clear error instead of a lookup miss.
func delegationName(kind, name string) error {
	if !config.ValidName(name) {
		return fmt.Errorf("invalid delegation %s name %q", kind, name)
	}
	return nil
}

func newDelegationListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List delegation profiles", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := localDelegationConfig()
		if err != nil {
			return err
		}
		names := make([]string, 0, len(cfg.Delegation.Profiles))
		for name := range cfg.Delegation.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			marker := " "
			if name == cfg.Delegation.ActiveProfile {
				marker = "*"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", marker, name)
		}
		return nil
	}}
}

func newDelegationShowCmd() *cobra.Command {
	return &cobra.Command{Use: "show [profile]", Args: cobra.MaximumNArgs(1), Short: "Show a delegation profile", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := localDelegationConfig()
		if err != nil {
			return err
		}
		name := cfg.Delegation.ActiveProfile
		if len(args) == 1 {
			name = args[0]
			if err := delegationName("profile", name); err != nil {
				return err
			}
		}
		profile, ok := cfg.Delegation.Profiles[name]
		if !ok {
			return fmt.Errorf("delegation profile %q not found", name)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", name)
		roles := make([]string, 0, len(profile.Roles))
		for role := range profile.Roles {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			target := profile.Roles[role]
			model, source := cfg.RoleTargetModel(target)
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", role, target.Template, model, source, target.Effort)
		}
		return nil
	}}
}

func newDelegationRenderCmd() *cobra.Command {
	return &cobra.Command{Use: "render", Short: "Render injected delegation instructions", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := localDelegationConfig()
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), leomcp.DelegationBlock(cfg))
		return err
	}}
}

func newDelegationResolveCmd() *cobra.Command {
	return &cobra.Command{Use: "resolve <role>", Args: cobra.ExactArgs(1), Short: "Resolve a role", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := localDelegationConfig()
		if err != nil {
			return err
		}
		if err := delegationName("role", args[0]); err != nil {
			return err
		}
		r, err := cfg.ResolveRole(args[0])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", r.Role, r.Template, r.EffectiveModel, r.ModelSource, r.Effort)
		return err
	}}
}

func newDelegationUseCmd() *cobra.Command {
	return &cobra.Command{Use: "use <profile>", Args: cobra.ExactArgs(1), Short: "Activate a delegation profile", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := localDelegationConfig()
		if err != nil {
			return err
		}
		if err := delegationName("profile", args[0]); err != nil {
			return err
		}
		if _, ok := cfg.Delegation.Profiles[args[0]]; !ok {
			return fmt.Errorf("delegation profile %q not found", args[0])
		}
		cfg.Delegation.ActiveProfile = args[0]
		if err := saveConfig(cfg); err != nil {
			return err
		}
		for _, warning := range cfg.DelegationWarnings() {
			fmt.Fprintln(cmd.ErrOrStderr(), "WARN:", warning)
		}
		if !daemon.IsRunning(cfg.HomePath) {
			fmt.Fprintln(cmd.ErrOrStderr(), "WARN: daemon is not running; profile will apply when it starts")
			return nil
		}
		resp, err := daemon.Send(cmd.Context(), cfg.HomePath, "POST", "/config/reload", nil)
		if err != nil {
			return fmt.Errorf("sending reload: %w", err)
		}
		if !resp.OK {
			return fmt.Errorf("reload failed: %s", resp.Error)
		}
		return nil
	}}
}
