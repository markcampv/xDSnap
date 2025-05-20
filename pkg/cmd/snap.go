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

	logLevel := "debug"
	if config.EnableTrace {
		logLevel = "trace"
	}
	log.Printf("Creating privileged debug pod to set log level '%s'", logLevel)

	curlURL := fmt.Sprintf("http://localhost:19000/logging?level=%s", logLevel)
	ephName, err := kubeService.CreatePrivilegedDebugPod(config.PodName, config.ContainerName, []string{
		"curl", "-s", "-X", "POST", curlURL,
	})
	if err != nil {
		log.Printf("Failed to create debug pod: %v", err)
	} else {
		err = kubeService.WaitForPodRunning(ephName, 30*time.Second)
		if err != nil {
			log.Printf("Timeout waiting for debug pod: %v", err)
		} else {
			var curlOut bytes.Buffer
			_, err := kubeService.ExecuteCommand(ephName, "debug", []string{
				"nsenter", "--target", "1", "--net", "--", "curl", "-s", "-X", "POST", curlURL,
			}, &curlOut)
			if err != nil {
				log.Printf("Failed to execute curl: %v", err)
			} else {
				log.Printf("curl output: %s", curlOut.String())
			}
		}
		_ = kubeService.DeletePod(ephName)
	}

	var tcpdumpPodName string
	if config.TcpdumpEnabled {
		log.Printf("Launching tcpdump capture pod...")
		tcpdumpPodName, err = kubeService.CreateConcurrentTcpdumpCapturePod(config.PodName, []string{
			config.ContainerName, "envoy-sidecar", "consul-dataplane",
		}, config.Duration)
		if err != nil {
			log.Printf("Failed to launch tcpdump capture pod: %v", err)
		} else {
			log.Printf("Waiting for tcpdump pod to complete...")
			time.Sleep(config.Duration + 5*time.Second)

			log.Printf("Fetching .pcap files from tcpdump pod: %s", tcpdumpPodName)
			var tarOut bytes.Buffer
			var tarErr bytes.Buffer
			_, err := kubeService.ExecuteCommandWithStderr(tcpdumpPodName, "tcpdump", []string{
				"tar", "-cf", "-", "-C", "/captures", ".",
			}, &tarOut, &tarErr)
			if err != nil {
				log.Printf("Failed to fetch .pcap files: %v", err)
				log.Printf("Stderr: %s", tarErr.String())
			} else {
				tarReader := tar.NewReader(&tarOut)
				for {
					hdr, err := tarReader.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						log.Printf("Error reading tar stream: %v", err)
						break
					}
					if hdr.FileInfo().IsDir() {
						continue
					}
					targetFile := filepath.Join(tempDir, hdr.Name)
					f, err := os.Create(targetFile)
					if err != nil {
						log.Printf("Failed to create file for pcap: %v", err)
						continue
					}
					_, err = io.Copy(f, tarReader)
					f.Close()
					if err != nil {
						log.Printf("Failed to write pcap file: %v", err)
					}
					log.Printf("Saved .pcap file: %s", targetFile)
				}
			}
			_ = kubeService.DeletePod(tcpdumpPodName)
		}
	}

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

	for i := 0; i < cap(logResults); i++ {
		<-logResults
	}

	tarFilePath := filepath.Join(config.OutputDir, fmt.Sprintf("%s_snapshot.tar.gz", config.PodName))
	if err := createTarGz(tarFilePath, tempDir); err != nil {
		return fmt.Errorf("failed to create tar.gz file: %w", err)
	}

	fmt.Printf("Snapshot for %s saved as %s\n", config.PodName, tarFilePath)

	if !config.SkipLogLevelReset {
		resetURL := "http://localhost:19000/logging?level=info"
		log.Printf("Resetting log level back to 'info' on pod: %s", config.PodName)

		resetPodName, err := kubeService.CreatePrivilegedDebugPod(config.PodName, config.ContainerName, []string{
			"curl", "-s", "-X", "POST", resetURL,
		})
		if err != nil {
			log.Printf("Failed to create pod to reset log level: %v", err)
		} else {
			err = kubeService.WaitForPodRunning(resetPodName, 30*time.Second)
			if err != nil {
				log.Printf("Timeout waiting for reset pod: %v", err)
			} else {
				var resetOut bytes.Buffer
				_, err := kubeService.ExecuteCommand(resetPodName, "debug", []string{
					"nsenter", "--target", "1", "--net", "--", "curl", "-s", "-X", "POST", resetURL,
				}, &resetOut)
				if err != nil {
					log.Printf("Failed to reset log level to info: %v", err)
				} else {
					log.Printf("Log level reset response: %s", resetOut.String())
				}
			}
			_ = kubeService.DeletePod(resetPodName)
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
