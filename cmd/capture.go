
package main 

import (
    "log"
    "os"
    "github.com/spf13/cobra"
    "github.com/markcampv/xDSnap/pkg/cmd" // Import the core logic for snapshot
    "k8s.io/cli-runtime/pkg/genericclioptions"
)

func newCaptureCommand(streams genericclioptions.IOStreams) *cobra.Command {
    var podName, containerName string
    var endpoints []string
    var outputDir string

    cwd, err := os.Getwd()
    if err != nil {
        log.Fatalf("Failed to get current directory: %v", err)
    }
    outputDir = cwd

    captureCmd := &cobra.Command{
        Use:   "capture",
        Short: "Capture Envoy snapshots from a Consul service mesh",
        Run: func(cmd *cobra.Command, args []string) {
            config := cmd.SnapshotConfig{
                PodName:       podName,
                ContainerName: containerName,
                Endpoints:     endpoints,
                OutputDir:     outputDir,
            }

            if err := cmd.CaptureSnapshot(config); err != nil {
                log.Fatalf("Error capturing snapshot: %v", err)
            }
        },
    }

    captureCmd.Flags().StringVar(&podName, "pod", "", "Name of the pod")
    captureCmd.Flags().StringVar(&containerName, "container", "", "Name of the container")
    captureCmd.Flags().StringSliceVar(&endpoints, "endpoints", []string{}, "Envoy endpoints to capture")
    captureCmd.Flags().StringVar(&outputDir, "output-dir", outputDir, "Directory to save snapshots")

    return captureCmd
}

func NewRootCommand(streams genericclioptions.IOStreams) *cobra.Command {
    rootCmd := &cobra.Command{
        Use:   "xdsnap",
        Short: "XDSnap captures Envoy state snapshots across Kubernetes pods for troubleshooting.",
    }

    rootCmd.AddCommand(newCaptureCommand(streams))
    return rootCmd
}
