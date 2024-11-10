package cmd

import (
    "context"
    "log"
    "time"
    "os"
    "path/filepath"
    "github.com/spf13/cobra"
    "github.com/markcampv/xDSnap/kube"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/clientcmd"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/cli-runtime/pkg/genericclioptions"
    "github.com/spf13/viper"
)

func NewCaptureCommand(streams genericclioptions.IOStreams) *cobra.Command {
    var podName, containerName, namespace string
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
            var config *rest.Config
            var err error

            // Try in-cluster config first
            config, err = rest.InClusterConfig()
            if err != nil {
                log.Println("Could not use in-cluster config, falling back to kubeconfig:", err)
                // Fallback to kubeconfig file
                kubeconfigPath := filepath.Join(os.Getenv("HOME"), ".kube", "config")
                config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
                if err != nil {
                    log.Fatalf("Failed to load kubeconfig: %v", err)
                }
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
             
            log.Printf("Listing pods in namespace: %s", namespace)

            if podName == "" {
                pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
                if err != nil {
                    log.Fatalf("Error listing pods: %v", err)
                }

                log.Printf("Number of pods found: %d", len(pods.Items))

                for _, pod := range pods.Items {
                    log.Printf("Pod %s annotations: %v", pod.Name, pod.Annotations)
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

            for _, pod := range podsToCapture {
                snapshotConfig := SnapshotConfig{
                    PodName:       pod,
                    ContainerName: containerName,
                    Endpoints:     endpoints,
                    OutputDir:     outputDir,
                }
                if err := CaptureSnapshot(kubeService, snapshotConfig); err != nil {
                    log.Printf("Error capturing snapshot for pod %s: %v", pod, err)
                }

            }
        },
    }

    captureCmd.Flags().StringVar(&podName, "pod", "", "Name of the pod (optional, will capture all pods with connect-inject=true if not specified)")
    captureCmd.Flags().StringVar(&containerName, "container", "", "Name of the container")
    captureCmd.Flags().StringSliceVar(&endpoints, "endpoints", []string{}, "Envoy endpoints to capture")
    captureCmd.Flags().StringVar(&outputDir, "output-dir", outputDir, "Directory to save snapshots")
    captureCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace to filter pods (optional)")
    _ = viper.BindEnv("namespace", "KUBECTL_PLUGINS_CURRENT_NAMESPACE")
    _ = viper.BindPFlag("namespace", captureCmd.Flags().Lookup("namespace"))

    return captureCmd
}



