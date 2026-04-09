package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage leo configuration",
	}

	cmd.AddCommand(newConfigShowCmd())

	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var raw bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display effective config with defaults applied",
		RunE: func(cmd *cobra.Command, args []string) error {
			if raw {
				// Show raw file contents
				path := cfgFile
				if path == "" {
					p, err := config.FindConfig("")
					if err != nil {
						return fmt.Errorf("no leo.yaml found")
					}
					path = p
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("reading config: %w", err)
				}
				fmt.Print(string(data))
				return nil
			}

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Apply defaults for display
			if cfg.Defaults.Model == "" {
				cfg.Defaults.Model = "sonnet"
			}
			if cfg.Defaults.MaxTurns == 0 {
				cfg.Defaults.MaxTurns = 15
			}

			fmt.Printf("# Home: %s\n", cfg.HomePath)
			fmt.Printf("# Default workspace: %s\n\n", cfg.DefaultWorkspace())

			data, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("marshaling config: %w", err)
			}

			// Also show resolved paths
			path := filepath.Join(cfg.HomePath, "leo.yaml")
			fmt.Printf("# Config file: %s\n", path)
			fmt.Print(string(data))
			return nil
		},
	}

	cmd.Flags().BoolVar(&raw, "raw", false, "show raw file contents without applying defaults")

	return cmd
}
