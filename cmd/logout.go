package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/config"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored API key",
	Args:  cobra.NoArgs,
	RunE:  runLogout,
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}

func runLogout(cmd *cobra.Command, args []string) error {
	if _, err := config.Load(cfgFile); err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := config.DeleteAuthToken(); err != nil {
		return fmt.Errorf("remove API key: %w", err)
	}

	fmt.Println("Logged out. API key removed from config.")
	return nil
}
