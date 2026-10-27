package cli

import (
	"errors"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/spf13/cobra"
)

// overrideCommand is the CLI command that makes a lead entry editable.
func overrideCommand(kind config.EntryKind, name string) string {
	switch kind {
	case config.KindTemplate:
		return "leo template override " + name
	case config.KindProfile:
		return "leo delegation override " + name
	}
	return fmt.Sprintf("leo config override %s %s", kind, name)
}

// withOverrideHint adds the override command to an owned_by_lead refusal.
func withOverrideHint(err error) error {
	var owned *config.OwnedByLeadError
	if !errors.As(err, &owned) {
		return err
	}
	return fmt.Errorf("%w\nhint: run '%s' to copy it into leo.yaml, then edit it there", err, overrideCommand(owned.Kind, owned.Name))
}

// overrideEntry copies a lead entry into the local leo.yaml, where it can be
// edited; the lead's copy then no longer applies on this host.
func overrideEntry(cmd *cobra.Command, kind config.EntryKind, name string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.OverrideLocally(kind, name); err != nil {
		return err
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %q from lead %q is now a local override in leo.yaml; edit it there, or delete it to follow the lead again.\n", kind, name, cfg.FollowLead())
	return nil
}

func newOverrideCmd(use, short string, kind config.EntryKind) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return overrideEntry(cmd, kind, args[0])
		},
	}
}

func newTemplateOverrideCmd() *cobra.Command {
	return newOverrideCmd("override", "Copy a template from the lead into leo.yaml to edit it locally", config.KindTemplate)
}

func newDelegationOverrideCmd() *cobra.Command {
	return newOverrideCmd("override", "Copy a delegation profile from the lead into leo.yaml to edit it locally", config.KindProfile)
}

// newConfigOverrideCmd is `leo config override {template|environment|profile} <name>`.
func newConfigOverrideCmd() *cobra.Command {
	kinds := []config.EntryKind{config.KindTemplate, config.KindEnvironment, config.KindProfile}
	cmd := &cobra.Command{
		Use:   "override {template|environment|profile} <name>",
		Short: "Copy an entry from the lead into leo.yaml to edit it locally",
		Long: `On a follower (a leo.yaml with a follow: block), templates, environments
and delegation profiles mirrored from the lead are read-only. override copies
one into leo.yaml as a local override, which replaces the lead's entry of the
same name until it is deleted. See docs/configuration/follow-lead.md.`,
		Example: `  leo config override template coder
  leo config override environment work`,
		Args:      cobra.ExactArgs(2),
		ValidArgs: []string{"template", "environment", "profile"},
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, kind := range kinds {
				if string(kind) == args[0] {
					return overrideEntry(cmd, kind, args[1])
				}
			}
			return fmt.Errorf("unknown entry kind %q: want template, environment or profile", args[0])
		},
	}
	return cmd
}
