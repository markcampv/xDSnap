package main

import (
    "context"
    "log"
    "os"

    "github.com/spf13/cobra"
    "github.com/markcampv/xDSnap/pkg/cmd"
    "github.com/markcampv/xDSnap/kube"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
            // Initialize Kubernetes client and config
            config, err := rest.InClusterConfig()
            if err != nil {
                log.Fatalf("Error creating in-cluster config: %v", err)
            }

            clientset, err := kubernetes.NewForConfig(config)
            if err != nil {
                log.Fatalf("Error creating Kubernetes client: %v", err)
            }

            // Initialize Kubernetes API service
            kubeService := kube.NewKubernetesApiService(clientset, config, "default") // Replace "default" with target namespace

            var podsToCapture []string

            if podName == "" {
                // No specific pod provided, so fetch pods with the required annotation
                pods, err := clientset.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{}) // Replace "default" with target namespace
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
                // Specific pod was provided, so only capture data from that pod
                podsToCapture = append(podsToCapture, podName)
            }

            // Run CaptureSnapshot for each targeted pod
            for _, pod := range podsToCapture {
                snapshotConfig := cmd.SnapshotConfig{
                    PodName:       pod,
                    ContainerName: containerName,
                    Endpoints:     endpoints,
                    OutputDir:     outputDir,
                }

                if err := cmd.CaptureSnapshot(kubeService, snapshotConfig); err != nil {
                    log.Printf("Error capturing snapshot for pod %s: %v", pod, err)
                }
            }
        },
    }

    captureCmd.Flags().StringVar(&podName, "pod", "", "Name of the pod (optional, will capture all pods with connect-inject=true if not specified)")
    captureCmd.Flags().StringVar(&containerName, "container", "", "Name of the container")
    captureCmd.Flags().StringSliceVar(&endpoints, "endpoints", []string{}, "Envoy endpoints to capture")
    captureCmd.Flags().StringVar(&outputDir, "output-dir", outputDir, "Directory to save snapshots")

    return captureCmd
}

