package kube

import (
    "context"
    "io"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/remotecommand"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "log"
)

type KubernetesApiService interface {
    ExecuteCommand(pod, container string, command []string, output io.Writer) (int, error)
}

type KubernetesApiServiceImpl struct {
    clientset  *kubernetes.Clientset
    restConfig *rest.Config
    namespace  string
}

// NewKubernetesApiService initializes the KubernetesApiService implementation
func NewKubernetesApiService(clientset *kubernetes.Clientset, restConfig *rest.Config, namespace string) KubernetesApiService {
    return &KubernetesApiServiceImpl{
        clientset:  clientset,
        restConfig: restConfig,
        namespace:  namespace,
    }
}

// ExecuteCommand executes a command in a specified pod/container and writes the output to an io.Writer
func (k *KubernetesApiServiceImpl) ExecuteCommand(pod, container string, command []string, output io.Writer) (int, error) {
    req := k.clientset.CoreV1().RESTClient().
        Post().
        Resource("pods").
        Name(pod).
        Namespace(k.namespace).
        SubResource("exec").
        Param("container", container).
        Param("stdin", "false").
        Param("stdout", "true").
        Param("stderr", "true").
        Param("tty", "false")

    // Add the command parameters
    for _, c := range command {
        req.Param("command", c)
    }

    exec, err := remotecommand.NewSPDYExecutor(k.restConfig, "POST", req.URL())
    if err != nil {
        log.Printf("Error creating SPDY executor: %v", err)
        return 1, err
    }

    // Stream the output to the provided Writer
    err = exec.Stream(remotecommand.StreamOptions{
        Stdout: output,
        Stderr: output,
    })
    if err != nil {
        log.Printf("Error streaming command output: %v", err)
        return 1, err
    }

    return 0, nil
}
