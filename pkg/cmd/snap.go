package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"github.com/markcampv/xDSnap/kube"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

type SnapshotConfig struct {
	PodName       string
	ContainerName string
	Endpoints     []string
	OutputDir     string
}

var DefaultEndpoints = []string{"/stats", "/config_dump", "/listeners", "/clusters"}

func CaptureSnapshot(kubeService kube.KubernetesApiService, config SnapshotConfig) error {
	if len(config.Endpoints) == 0 {
		config.Endpoints = DefaultEndpoints
	}

	tempDir, err := os.MkdirTemp("", config.PodName)
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir) // Clean up temp directory after tar is created

	// Capture each endpoint and write to individual JSON files in the temp directory
	for _, endpoint := range config.Endpoints {
		data, err := fetchEnvoyEndpoint(kubeService, config.PodName, config.ContainerName, endpoint)
		if err != nil {
			log.Printf("Error capturing %s: %v", endpoint, err)
			continue
		}

		// Check if data is empty, indicating potential issues with the endpoint
		if len(data) == 0 {
			log.Printf("Warning: No data received from endpoint %s for pod %s", endpoint, config.PodName)
			continue
		}

		filePath := filepath.Join(tempDir, fmt.Sprintf("%s.json", endpoint[1:]))
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			log.Printf("Failed to write data for %s: %v", endpoint, err)
		} else {
			fmt.Printf("Captured %s for %s and saved to %s\n", endpoint, config.PodName, filePath)
		}
	}

	// Create tar.gz file in the output directory
	tarFilePath := filepath.Join(config.OutputDir, fmt.Sprintf("%s_snapshot.tar.gz", config.PodName))
	err = createTarGz(tarFilePath, tempDir)
	if err != nil {
		return fmt.Errorf("failed to create tar.gz file: %w", err)
	}

	fmt.Printf("Snapshot for %s saved as %s\n", config.PodName, tarFilePath)
	return nil
}

func fetchEnvoyEndpoint(kubeService kube.KubernetesApiService, pod, container, endpoint string) ([]byte, error) {
	command := []string{"wget", "-qO-", fmt.Sprintf("http://localhost:19000%s", endpoint)}
	var outputBuffer bytes.Buffer

	const maxRetries = 5               // Increased retry count for interval use
	const retryDelay = 3 * time.Second // Delay for retries
	var err error

	for i := 0; i < maxRetries; i++ {
		outputBuffer.Reset()
		log.Printf("Fetching data from %s on pod %s, attempt %d", endpoint, pod, i+1)

		if _, err = kubeService.ExecuteCommand(pod, container, command, &outputBuffer); err != nil {
			time.Sleep(retryDelay)
			continue
		}

		outputBytes := outputBuffer.Bytes()
		if len(outputBytes) > 0 {
			return outputBytes, nil
		}

		time.Sleep(retryDelay)
	}

	return nil, fmt.Errorf("failed to fetch data from endpoint %s after %d retries", endpoint, maxRetries)
}

// createTarGz compresses a directory into a tar.gz file
func createTarGz(outputFile string, sourceDir string) error {
	tarFile, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	gzipWriter := gzip.NewWriter(tarFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	err = filepath.Walk(sourceDir, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if fi.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(sourceDir, file)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(fi, relPath)
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tarWriter, f)
		return err
	})

	return err
}
