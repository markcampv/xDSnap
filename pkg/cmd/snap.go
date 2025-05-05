package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/markcampv/xDSnap/kube"
)

type SnapshotConfig struct {
	PodName       string
	ContainerName string
	Endpoints     []string
	OutputDir     string
	ExtraLogs     []string
	Duration      time.Duration
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
	defer os.RemoveAll(tempDir)

	// Set envoy log level to debug
	_, err = kubeService.ExecuteCommand(config.PodName, config.ContainerName, []string{
		"wget", "-O", "-", "--post-data", "level=debug", "http://localhost:19000/logging"}, io.Discard)
	if err != nil {
		log.Printf("Failed to set envoy log level to debug from container %s: %v", config.ContainerName, err)
	}

	// Start log streaming concurrently before fetching data
	logResults := make(chan struct{}, len(config.ExtraLogs)+1)
	for _, c := range append([]string{config.ContainerName}, config.ExtraLogs...) {
		if c == "" {
			logResults <- struct{}{}
			continue
		}
		c := c
		go func() {
			logBytes, err := streamLogsWithTimeout(kubeService, config.PodName, c, config.Duration+10*time.Second)
			if err != nil {
				log.Printf("Failed to stream logs for container %s: %v", c, err)
			} else {
				logsPath := filepath.Join(tempDir, fmt.Sprintf("%s-logs.txt", c))
				if err := os.WriteFile(logsPath, logBytes, 0644); err != nil {
					log.Printf("Failed to write logs for container %s: %v", c, err)
				}
			}
			logResults <- struct{}{}
		}()
	}

	// Fetch snapshot data while logs are streaming
	for _, endpoint := range config.Endpoints {
		data, err := fetchEnvoyEndpoint(kubeService, config.PodName, config.ContainerName, endpoint)
		if err != nil {
			log.Printf("Error capturing %s: %v", endpoint, err)
			continue
		}

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

	// Wait for all log collection to finish
	for i := 0; i < cap(logResults); i++ {
		<-logResults
	}

	// Revert envoy log level to info
	_, err = kubeService.ExecuteCommand(config.PodName, config.ContainerName, []string{
		"wget", "-O", "-", "--post-data", "level=info", "http://localhost:19000/logging"}, io.Discard)
	if err != nil {
		log.Printf("Failed to revert envoy log level to info from container %s: %v", config.ContainerName, err)
	}

	tarFilePath := filepath.Join(config.OutputDir, fmt.Sprintf("%s_snapshot.tar.gz", config.PodName))
	if err := createTarGz(tarFilePath, tempDir); err != nil {
		return fmt.Errorf("failed to create tar.gz file: %w", err)
	}

	fmt.Printf("Snapshot for %s saved as %s\n", config.PodName, tarFilePath)
	return nil
}

func streamLogsWithTimeout(kubeService kube.KubernetesApiService, pod, container string, duration time.Duration) ([]byte, error) {
	var logsBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- kubeService.FetchContainerLogs(ctx, pod, container, true, &logsBuf)
	}()

	select {
	case <-ctx.Done():
		return logsBuf.Bytes(), nil
	case err := <-done:
		return logsBuf.Bytes(), err
	}
}

func fetchEnvoyEndpoint(kubeService kube.KubernetesApiService, pod, container, endpoint string) ([]byte, error) {
	command := []string{"wget", "-qO-", fmt.Sprintf("http://localhost:19000%s", endpoint)}
	var outputBuffer bytes.Buffer

	const maxRetries = 5
	const retryDelay = 3 * time.Second

	for i := 0; i < maxRetries; i++ {
		outputBuffer.Reset()
		log.Printf("Fetching data from %s on pod %s, attempt %d", endpoint, pod, i+1)

		_, err := kubeService.ExecuteCommand(pod, container, command, &outputBuffer)
		if err == nil && outputBuffer.Len() > 0 {
			return outputBuffer.Bytes(), nil
		}

		time.Sleep(retryDelay)
	}

	return nil, fmt.Errorf("failed to fetch data from endpoint %s after %d retries", endpoint, maxRetries)
}

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
