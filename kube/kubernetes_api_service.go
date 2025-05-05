package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type KubernetesApiService interface {
	ExecuteCommand(pod, container string, command []string, output io.Writer) (int, error)
	FetchContainerLogs(ctx context.Context, podName, containerName string, follow bool, out io.Writer) error
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
func (k *KubernetesApiServiceImpl) ExecuteCommand(podName, containerName string, command []string, stdOut io.Writer) (int, error) {
	log.Printf("Executing command in pod %s, container %s: %v", podName, containerName, command)

	var stdErr bytes.Buffer
	req := k.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(k.namespace).
		SubResource("exec").
		Param("container", containerName).
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "false")
	for _, arg := range command {
		req.Param("command", arg)
	}

	executor, err := remotecommand.NewSPDYExecutor(k.restConfig, "POST", req.URL())
	if err != nil {
		return 0, fmt.Errorf("failed to create SPDY executor: %w", err)
	}

	err = executor.Stream(remotecommand.StreamOptions{
		Stdout: stdOut,
		Stderr: &stdErr,
		Tty:    false,
	})

	if stdErr.Len() > 0 {
		log.Printf("Stderr from pod %s, container %s: %s", podName, containerName, stdErr.String())
	}

	if err != nil {
		return 0, fmt.Errorf("command execution failed: %w", err)
	}

	return 0, nil
}

// FetchContainerLogs streams logs from a container and writes them to an io.Writer
func (k *KubernetesApiServiceImpl) FetchContainerLogs(ctx context.Context, podName, containerName string, follow bool, out io.Writer) error {
	req := k.clientset.CoreV1().Pods(k.namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		Follow:    follow,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("error opening log stream: %w", err)
	}
	defer stream.Close()

	_, err = io.Copy(out, stream)
	if err != nil {
		return fmt.Errorf("error copying log output: %w", err)
	}
	return nil
}
