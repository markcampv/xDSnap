// pkg/cmd/root.go
package cmd

import (
    "github.com/spf13/cobra"
)

// NewRootCommand initializes the root command for the CLI
func NewRootCommand() *cobra.Command {
    rootCmd := &cobra.Command{
        Use:   "xdsnap",
        Short: "xDSnap is a CLI tool for capturing Envoy snapshots",
    }

    // Add subcommands to the root command
    rootCmd.AddCommand(NewCaptureCommand()) 

    return rootCmd
}
