package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

	cmd.AddCommand(newConfigShowCmd(), newConfigEditCmd(), newConfigPathCmd())

	return cmd
}

// newConfigPathCmd prints the absolute path to the resolved config file.
// Useful for scripting, e.g. `vim $(leo config path)`.
func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the absolute path to the resolved leo.yaml",
		Long:  "Print the absolute path to the leo.yaml that will be used, respecting --config and the normal resolution rules.",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := cfgFile
			if path == "" {
				p, err := config.FindConfig("")
				if err != nil {
					return fmt.Errorf("no leo.yaml found — run 'leo setup' first: %w", err)
				}
				path = p
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving config path: %w", err)
			}
			fmt.Println(abs)
			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	var raw bool
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display effective config with defaults applied",
		RunE: func(cmd *cobra.Command, args []string) error {
			if raw && asJSON {
				return fmt.Errorf("--raw and --json are mutually exclusive")
			}
			if asJSON {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if cfg.Defaults.Model == "" {
					cfg.Defaults.Model = "sonnet"
				}
				if cfg.Defaults.MaxTurns == 0 {
					cfg.Defaults.MaxTurns = 15
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(cfg)
			}
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON (suitable for scripting); defaults are applied")

	return cmd
}

func newConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open leo.yaml in your editor",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := cfgFile
			if path == "" {
				p, err := config.FindConfig("")
				if err != nil {
					// Fall back to default location
					path = filepath.Join(config.DefaultHome(), "leo.yaml")
				} else {
					path = p
				}
			}

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}

			editorCmd := exec.Command(editor, path)
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = os.Stdout
			editorCmd.Stderr = os.Stderr
			return editorCmd.Run()
		},
	}
}
