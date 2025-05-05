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
	var interval, duration int

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get current directory: %v", err)
	}
	outputDir = cwd

	captureCmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture Envoy snapshots from a Consul service mesh",
		Run: func(cmd *cobra.Command, args []string) {
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
			if duration <= 0 {
				log.Fatalf("Duration must be greater than 0 seconds")
			}

			ticker := time.NewTicker(time.Duration(interval) * time.Second)
			defer ticker.Stop()

			stopChan := make(chan struct{})
			go func() {
				timeout := time.After(time.Duration(duration) * time.Second)
				for {
					select {
					case <-timeout:
						close(stopChan)
						log.Println("Duration ended, stopping capture")
						return
					case <-ticker.C:
						timestamp := time.Now().Format("20060102_150405")
						snapshotDir := fmt.Sprintf("%s/snapshot_%s", outputDir, timestamp)

						if err := os.MkdirAll(snapshotDir, 0755); err != nil {
							log.Printf("Failed to create snapshot directory: %v", err)
							continue
						}

						for _, pod := range podsToCapture {
							snapshotConfig := SnapshotConfig{
								PodName:       pod,
								ContainerName: containerName,
								Endpoints:     endpoints,
								OutputDir:     snapshotDir,
								ExtraLogs:     []string{"consul-dataplane"},
							}
							if err := CaptureSnapshot(kubeService, snapshotConfig); err != nil {
								log.Printf("Error capturing snapshot for pod %s: %v", pod, err)
							}
						}
					}
				}
			}()

			<-stopChan
		},
	}

	captureCmd.Flags().StringVar(&podName, "pod", "", "Name of the pod (optional, will capture all pods with connect-inject=true if not specified)")
	captureCmd.Flags().StringVar(&containerName, "container", "", "Name of the container")
	captureCmd.Flags().StringSliceVar(&endpoints, "endpoints", []string{}, "Envoy endpoints to capture")
	captureCmd.Flags().StringVar(&outputDir, "output-dir", outputDir, "Directory to save snapshots")
	captureCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace to filter pods (optional)")
	captureCmd.Flags().IntVar(&interval, "interval", 30, "Interval (in seconds) at which to capture data, minimum 5 seconds")
	captureCmd.Flags().IntVar(&duration, "duration", 60, "Duration (in seconds) to run the capture process")

	_ = viper.BindEnv("namespace", "KUBECTL_PLUGINS_CURRENT_NAMESPACE")
	_ = viper.BindPFlag("namespace", captureCmd.Flags().Lookup("namespace"))

	return captureCmd
}
