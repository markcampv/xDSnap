package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/markcampv/xDSnap/kube"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func NewCaptureCommand(streams genericclioptions.IOStreams) *cobra.Command {
	var podName, containerName, namespace string
	var endpoints []string
	var outputDir string
	var interval, duration, repeat int
	var enableTrace, tcpdumpEnabled bool

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get current directory: %v", err)
	}
	outputDir = cwd

	captureCmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture Envoy snapshots from a Consul service mesh",
		Run: func(cmd *cobra.Command, args []string) {
			if containerName == "consul-dataplane" {
				log.Fatal("Error: 'consul-dataplane' cannot be used as the --container value. Please specify the application container instead.")
			}

			config, err := rest.InClusterConfig()
			if err != nil {
				log.Printf("Could not use in-cluster config, falling back to kubeconfig: %v", err)
				configFlags := genericclioptions.NewConfigFlags(true)
				kubeconfig := os.Getenv("KUBECONFIG")
				configFlags.KubeConfig = &kubeconfig
				restConfig, err := configFlags.ToRESTConfig()
				if err != nil {
					log.Fatalf("Error creating Kubernetes client config: %v", err)
				}
				config = restConfig
			}

			clientset, err := kubernetes.NewForConfig(config)
			if err != nil {
				log.Fatalf("Error creating Kubernetes client: %v", err)
			}

			if namespace == "" {
				namespace = "default"
			}

			kubeService := kube.NewKubernetesApiService(clientset, config, namespace)

			var podsToCapture []string
			if podName == "" {
				pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					log.Fatalf("Error listing pods: %v", err)
				}
				for _, pod := range pods.Items {
					if pod.Annotations["consul.hashicorp.com/connect-inject"] == "true" {
						podsToCapture = append(podsToCapture, pod.Name)
					}
				}
				if len(podsToCapture) == 0 {
					log.Println("No pods found with the annotation consul.hashicorp.com/connect-inject=true")
					return
				}
			} else {
				podsToCapture = append(podsToCapture, podName)
			}

			if interval < 5 {
				log.Fatalf("Interval must be at least 5 seconds")
			}

			if repeat > 0 {
				log.Printf("Starting snapshot capture with sleep=%ds repeat=%d trace=%v tcpdump=%v outputDir=%s",
					interval, repeat, enableTrace, tcpdumpEnabled, outputDir)
			} else {
				log.Printf("Starting snapshot capture with sleep=%ds duration=%ds trace=%v tcpdump=%v outputDir=%s",
					interval, duration, enableTrace, tcpdumpEnabled, outputDir)
			}

			captures := 0
			var startTime time.Time

			for {
				if repeat > 0 && captures >= repeat {
					log.Println("Repeat count reached, stopping capture")
					break
				}

				// Delay setting the duration timer until after first snapshot begins
				if repeat == 0 && duration > 0 && !startTime.IsZero() && time.Since(startTime) >= time.Duration(duration)*time.Second {
					log.Println("Duration ended, stopping capture")
					break
				}

				timestamp := time.Now().Format("20060102_150405")
				snapshotDir := fmt.Sprintf("%s/snapshot_%s", outputDir, timestamp)

				if err := os.MkdirAll(snapshotDir, 0755); err != nil {
					log.Printf("Failed to create snapshot directory: %v", err)
					continue
				}

				for _, pod := range podsToCapture {
					containers, err := kubeService.ListContainers(pod)
					if err != nil {
						log.Printf("Failed to list containers for pod %s: %v", pod, err)
						continue
					}

					sidecar := ""
					for _, c := range containers {
						if c == "consul-dataplane" || c == "envoy-sidecar" {
							sidecar = c
							break
						}
					}
					if sidecar == "" {
						log.Printf("No known Envoy sidecar found in pod %s", pod)
						continue
					}

					finalReset := repeat == 0 || captures == repeat-1

					log.Printf("Calling CaptureSnapshot -> pod: %s | container: %s | enableTrace: %v | tcpdump: %v | extraLogs: [%s] | finalReset: %v",
						pod, containerName, enableTrace, tcpdumpEnabled, sidecar, finalReset)

					snapshotConfig := SnapshotConfig{
						PodName:           pod,
						ContainerName:     containerName,
						Endpoints:         endpoints,
						OutputDir:         snapshotDir,
						ExtraLogs:         []string{sidecar},
						EnableTrace:       enableTrace,
						TcpdumpEnabled:    tcpdumpEnabled,
						Duration:          time.Duration(duration) * time.Second,
						SkipLogLevelReset: !finalReset,
					}

					// Start timer here *after* setup begins
					if repeat == 0 && duration > 0 && startTime.IsZero() {
						startTime = time.Now()
					}

					if err := CaptureSnapshot(kubeService, snapshotConfig); err != nil {
						log.Printf("Error capturing snapshot for pod %s: %v", pod, err)
					}
				}

				captures++

				if repeat > 0 && captures < repeat {
					log.Printf("Sleeping %ds before next snapshot (repeat mode)", interval)
					time.Sleep(time.Duration(interval) * time.Second)
				} else if repeat == 0 {
					time.Sleep(time.Duration(interval) * time.Second)
				}
			}
		},
	}

	captureCmd.Flags().StringVar(&podName, "pod", "", "Pod name (optional; defaults to all pods with connect-inject=true)")
	captureCmd.Flags().StringVar(&containerName, "container", "", "Name of the application container")
	captureCmd.Flags().StringSliceVar(&endpoints, "endpoints", []string{}, "Envoy endpoints to capture")
	captureCmd.Flags().StringVar(&outputDir, "output-dir", outputDir, "Directory to save snapshots")
	captureCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Target namespace (optional)")
	captureCmd.Flags().IntVar(&interval, "sleep", 5, "Sleep duration between captures in seconds (minimum 5s)")
	captureCmd.Flags().IntVar(&duration, "duration", 60, "Total capture duration in seconds")
	captureCmd.Flags().IntVar(&repeat, "repeat", 0, "Number of snapshot repetitions (takes precedence over duration)")
	captureCmd.Flags().BoolVar(&enableTrace, "enable-trace", false, "Enable Envoy trace log level")
	captureCmd.Flags().BoolVar(&tcpdumpEnabled, "tcpdump", false, "Enable tcpdump capture (runs once if enabled)")

	_ = viper.BindEnv("namespace", "KUBECTL_PLUGINS_CURRENT_NAMESPACE")
	_ = viper.BindPFlag("namespace", captureCmd.Flags().Lookup("namespace"))

	return captureCmd
}
