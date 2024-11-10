// pkg/cmd/root.go
package cmd

import (
    "github.com/spf13/cobra"
    "k8s.io/cli-runtime/pkg/genericclioptions"
)

// NewRootCommand creates the root command for xDSnap
func NewRootCommand(streams genericclioptions.IOStreams) *cobra.Command {
    rootCmd := &cobra.Command{
        Use:   "xdsnap",
        Short: "XDSnap captures Envoy state snapshots across Kubernetes pods for troubleshooting.",
    }

    // Add the capture subcommand
    rootCmd.AddCommand(NewCaptureCommand(streams))

    return rootCmd
}

