package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/justtunnel/justtunnel-cli/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("justtunnel %s\n", version.Version)
		fmt.Printf("  commit: %s\n", version.Commit)
		fmt.Printf("  built:  %s\n", version.Date)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
