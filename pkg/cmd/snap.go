package cmd

import (
    "bytes"
    "fmt"
    "log"
    "os"
    "path/filepath"
    "github.com/markcampv/xDSnap/kube"
)

type SnapshotConfig struct {
    PodName       string
    ContainerName string
    Endpoints     []string
    OutputDir     string
}

// DefaultEndpoints defines the Envoy endpoints to capture if none are specified
var DefaultEndpoints = []string{"/stats", "/config_dump", "/listeners", "/clusters"}

// CaptureSnapshot captures Envoy data from specified endpoints and saves it
func CaptureSnapshot(kubeService kube.KubernetesApiService, config SnapshotConfig) error {
    if len(config.Endpoints) == 0 {
        config.Endpoints = DefaultEndpoints
    }

    // Ensure the output directory exists
    if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
        return fmt.Errorf("failed to create output directory: %w", err)
    }

    for _, endpoint := range config.Endpoints {
        // Fetch data from each endpoint
        data, err := fetchEnvoyEndpoint(kubeService, config.PodName, config.ContainerName, endpoint)
        if err != nil {
            log.Printf("Error capturing %s: %v", endpoint, err)
            continue
        }

        // Save the data to a file in the output directory
        filePath := filepath.Join(config.OutputDir, fmt.Sprintf("%s_%s.json", config.PodName, endpoint))
        if err := os.WriteFile(filePath, data, 0644); err != nil {
            log.Printf("Failed to write data for %s: %v", endpoint, err)
        } else {
            fmt.Printf("Captured %s for %s and saved to %s\n", endpoint, config.PodName, filePath)
        }
    }
    return nil
}

// fetchEnvoyEndpoint fetches data from a specific Envoy endpoint using the Kubernetes API service
func fetchEnvoyEndpoint(kubeService kube.KubernetesApiService, pod, container, endpoint string) ([]byte, error) {
    command := []string{"curl", fmt.Sprintf("localhost:19000%s", endpoint)}
    var outputBuffer bytes.Buffer

    // Execute the command in the specified pod/container
    if _, err := kubeService.ExecuteCommand(pod, container, command, &outputBuffer); err != nil {
        return nil, err
    }

    return outputBuffer.Bytes(), nil
}
