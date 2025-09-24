package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/markcampv/xDSnap/kube"
)

type SnapshotConfig struct {
	PodName           string
	ContainerName     string
	Endpoints         []string
	OutputDir         string
	ExtraLogs         []string
	Duration          time.Duration
	EnableTrace       bool
	TcpdumpEnabled    bool
	SkipLogLevelReset bool
}

var DefaultEndpoints = []string{"/stats", "/config_dump", "/listeners", "/clusters", "/certs"}

func CaptureSnapshot(kubeService kube.KubernetesApiService, config SnapshotConfig) error {
	if len(config.Endpoints) == 0 {
		config.Endpoints = DefaultEndpoints
	}

	log.Printf("CaptureSnapshot called with Pod=%s Container=%s EnableTrace=%v", config.PodName, config.ContainerName, config.EnableTrace)

	tempDir, err := os.MkdirTemp("", config.PodName)
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Stream logs from app container + any extras (e.g., envoy-sidecar / consul-dataplane)
	logResults := make(chan struct{}, len(config.ExtraLogs)+1)
	for _, c := range append([]string{config.ContainerName}, config.ExtraLogs...) {
		if c == "" {
			logResults <- struct{}{}
			continue
		}
		c := c
		go func() {
			log.Printf("Starting log stream for container %s", c)
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

	// --- Set Envoy log level via EPHEMERAL container (no docker.sock) ---
	logLevel := "debug"
	if config.EnableTrace {
		logLevel = "trace"
	}
	log.Printf("Setting Envoy log level to '%s' via ephemeral container", logLevel)
	curlURL := fmt.Sprintf("http://127.0.0.1:19000/logging?level=%s", logLevel)
	if err := kubeService.RunEphemeralInTargetNetNS(
		config.PodName,
		config.ContainerName, // any container in the pod shares the netns
		[]string{"sh", "-c", "curl -s -X POST " + curlURL}, // simple POST to admin /logging
		false,
		30*time.Second,
	); err != nil {
		log.Printf("Failed to set log level: %v", err)
	}

	// --- Optional tcpdump capture (runtime-agnostic; streams base64 via logs) ---
	if config.TcpdumpEnabled {
		log.Printf("Starting tcpdump via ephemeral container (streaming to logs)...")
		ephemName, err := kubeService.CreateConcurrentTcpdumpCapturePod(
			config.PodName,
			[]string{config.ContainerName, "envoy-sidecar", "consul-dataplane"},
			config.Duration,
		)
		if err != nil {
			log.Printf("Failed to start tcpdump: %v", err)
		} else {
			// The ephemeral container completed; fetch its (base64) logs and decode to a .pcap
			var logsBuf bytes.Buffer
			if err := kubeService.FetchContainerLogs(context.Background(), config.PodName, ephemName, false, &logsBuf); err != nil {
				log.Printf("Failed to fetch tcpdump logs for %s: %v", ephemName, err)
			} else if logsBuf.Len() == 0 {
				log.Printf("No tcpdump data found in logs for %s", ephemName)
			} else {
				// Sanitize and decode base64 safely
				raw := logsBuf.String()
				clean := regexp.MustCompile(`[^A-Za-z0-9+/=]`).ReplaceAllString(strings.TrimSpace(raw), "")
				if clean == "" {
					log.Printf("No base64 tcpdump data after sanitization")
				} else {
					data, decErr := base64.StdEncoding.DecodeString(clean)
					if decErr != nil {
						log.Printf("Failed to decode base64 tcpdump stream (raw=%dB, clean=%dB): %v", len(raw), len(clean), decErr)
					} else {
						pcapPath := filepath.Join(tempDir, "xdsnap.pcap")
						if werr := os.WriteFile(pcapPath, data, 0644); werr != nil {
							log.Printf("Failed to write pcap file: %v", werr)
						} else {
							log.Printf("Saved .pcap file: %s", pcapPath)
						}
					}
				}
			}
		}
	}

	// --- Envoy admin endpoints via PORT-FORWARD (with exec fallback inside fetchEnvoyEndpoint) ---
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
		filePath := filepath.Join(tempDir, fmt.Sprintf("%s.json", strings.TrimPrefix(endpoint, "/")))
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			log.Panicf("Failed to write data for %s: %v", endpoint, err)
		} else {
			fmt.Printf("Captured %s for %s and saved to %s\n", endpoint, config.PodName, filePath)
		}
	}

	// Wait for all log streams to finish flushing
	for i := 0; i < cap(logResults); i++ {
		<-logResults
	}

	// Bundle snapshot
	tarFilePath := filepath.Join(config.OutputDir, fmt.Sprintf("%s_snapshot.tar.gz", config.PodName))
	if err := createTarGz(tarFilePath, tempDir); err != nil {
		return fmt.Errorf("failed to create tar.gz file: %w", err)
	}
	fmt.Printf("Snapshot for %s saved as %s\n", config.PodName, tarFilePath)

	// Reset log level via EPHEMERAL container
	if !config.SkipLogLevelReset {
		resetURL := "http://127.0.0.1:19000/logging?level=info"
		log.Printf("Resetting Envoy log level back to 'info' on pod: %s", config.PodName)
		if err := kubeService.RunEphemeralInTargetNetNS(
			config.PodName,
			config.ContainerName,
			[]string{"sh", "-c", "curl -s -X POST " + resetURL},
			false,
			30*time.Second,
		); err != nil {
			log.Printf("Failed to reset log level to info: %v", err)
		}
	}

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
	const podPort = 19000
	const maxRetries = 5
	const retryDelay = 2 * time.Second

	// --- First attempt: port-forward ---
	for i := 0; i < maxRetries; i++ {
		b, err := kubeService.PortForwardGET(pod, podPort, endpoint)
		if err == nil && len(b) > 0 {
			return b, nil
		}
		time.Sleep(retryDelay)
	}

	// --- Fallback: ephemeral curl inside pod netns ---
	var buf bytes.Buffer
	curlCmd := []string{
		"sh", "-c",
		fmt.Sprintf("curl -s http://127.0.0.1:%d%s", podPort, endpoint),
	}

	err := kubeService.RunEphemeralInTargetNetNSWithOutput(
		pod,
		container, // any container in the pod (shares netns)
		curlCmd,
		false,          // not privileged
		15*time.Second, // timeout
		&buf,           // capture stdout
		nil,            // ignore stderr
	)
	if err == nil && buf.Len() > 0 {
		log.Printf("Fetched %s from pod %s via ephemeral curl", endpoint, pod)
		return buf.Bytes(), nil
	}

	return nil, fmt.Errorf("port-forward and ephemeral curl both failed for %s", endpoint)
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
