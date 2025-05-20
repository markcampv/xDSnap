package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const NetshootImage = "campvin/netshoot-docker:latest"

type KubernetesApiService interface {
	ExecuteCommand(pod string, container string, command []string, output io.Writer) (int, error)
	ExecuteCommandWithStderr(pod string, container string, command []string, stdout, stderr io.Writer) (int, error)
	FetchContainerLogs(ctx context.Context, podName string, containerName string, follow bool, out io.Writer) error
	ListContainers(podName string) ([]string, error)
	InjectNetshootDebugContainer(targetPod string) error
	ContainerExists(podName, container string) (bool, error)
	LaunchEphemeralNetshoot(targetPod string, command []string) error
	CreateEphemeralNetshootPod(targetPod, container string, command []string) (string, error)
	CreatePrivilegedDebugPod(targetPod string, containerName string, command []string) (string, error)
	CreateConcurrentTcpdumpCapturePod(targetPod string, containers []string, duration time.Duration) (string, error)
	DeletePod(podName string) error
	WaitForPodRunning(podName string, timeout time.Duration) error
}

type KubernetesApiServiceImpl struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
	namespace  string
}

var _ KubernetesApiService = &KubernetesApiServiceImpl{}

func NewKubernetesApiService(clientset *kubernetes.Clientset, restConfig *rest.Config, namespace string) KubernetesApiService {
	return &KubernetesApiServiceImpl{
		clientset:  clientset,
		restConfig: restConfig,
		namespace:  namespace,
	}
}

func (k *KubernetesApiServiceImpl) ExecuteCommand(pod string, container string, command []string, output io.Writer) (int, error) {
	var stderr bytes.Buffer

	req := k.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(pod).
		Namespace(k.namespace).
		SubResource("exec").
		Param("container", container).
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "false")

	for _, arg := range command {
		req.Param("command", arg)
	}

	exec, err := remotecommand.NewSPDYExecutor(k.restConfig, "POST", req.URL())
	if err != nil {
		return 0, fmt.Errorf("failed to create executor: %w", err)
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdout: output,
		Stderr: &stderr,
		Tty:    false,
	})

	if err != nil {
		return 1, fmt.Errorf("command execution failed: %s", stderr.String())
	}

	return 0, nil
}

func (k *KubernetesApiServiceImpl) FetchContainerLogs(ctx context.Context, podName string, containerName string, follow bool, out io.Writer) error {
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
	return err
}

func (k *KubernetesApiServiceImpl) ListContainers(podName string) ([]string, error) {
	pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}
	var containers []string
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	return containers, nil
}

func (k *KubernetesApiServiceImpl) InjectNetshootDebugContainer(targetPod string) error {
	log.Printf("Injecting netshoot container into pod: %s", targetPod)
	return fmt.Errorf("not supported: cannot inject containers into an existing pod")
}

func (k *KubernetesApiServiceImpl) ContainerExists(podName, container string) (bool, error) {
	containers, err := k.ListContainers(podName)
	if err != nil {
		return false, err
	}
	for _, c := range containers {
		if c == container {
			return true, nil
		}
	}
	return false, nil
}

func (k *KubernetesApiServiceImpl) LaunchEphemeralNetshoot(targetPod string, command []string) error {
	_, err := k.CreateEphemeralNetshootPod(targetPod, "netshoot", command)
	return err
}

func (k *KubernetesApiServiceImpl) CreateEphemeralNetshootPod(targetPod, container string, command []string) (string, error) {
	target, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to fetch target pod: %w", err)
	}
	ephPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "netshoot-debug-",
			Namespace:    k.namespace,
			Labels:       map[string]string{"debug": "true"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      target.Spec.NodeName,
			Containers: []corev1.Container{
				{
					Name:            container,
					Image:           NetshootImage,
					Command:         command,
					ImagePullPolicy: corev1.PullAlways,
				},
			},
		},
	}

	pod, err := k.clientset.CoreV1().Pods(k.namespace).Create(context.TODO(), ephPod, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create ephemeral pod: %w", err)
	}

	return pod.Name, nil
}

func (k *KubernetesApiServiceImpl) DeletePod(podName string) error {
	return k.clientset.CoreV1().Pods(k.namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
}

func (k *KubernetesApiServiceImpl) WaitForPodRunning(podName string, timeout time.Duration) error {
	watcher, err := k.clientset.CoreV1().Pods(k.namespace).Watch(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.Set(map[string]string{"debug": "true"}).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to watch pods: %w", err)
	}
	defer watcher.Stop()
	timeoutCh := time.After(timeout)
	for {
		select {
		case <-timeoutCh:
			return fmt.Errorf("timeout waiting for pod %s to be running", podName)
		case event := <-watcher.ResultChan():
			pod, ok := event.Object.(*corev1.Pod)
			if !ok || pod.Name != podName {
				continue
			}
			if pod.Status.Phase == corev1.PodRunning {
				return nil
			}
		}
	}
}

func (k *KubernetesApiServiceImpl) CreatePrivilegedDebugPod(targetPod string, containerName string, command []string) (string, error) {
	log.Printf("Creating privileged debug pod targeting: %s (container: %s)", targetPod, containerName)

	target, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get target pod: %w", err)
	}

	if len(command) == 0 {
		return "", fmt.Errorf("no command specified for privileged pod")
	}

	grepPattern := fmt.Sprintf("k8s_%s_%s", containerName, targetPod)
	script := fmt.Sprintf(`#/bin/sh
set -e
CID=$(docker ps --format '{{.ID}} {{.Names}}' | grep "%s" | awk '{print $1}')
if [ -z "$CID" ]; then
  echo "No container ID found for %s"
  exit 1
fi
PID=$(docker inspect -f '{{.State.Pid}}' "$CID")
echo "Found PID: $PID"
nsenter --target "$PID" --net -- %s
`, grepPattern, grepPattern, strings.Join(command, " "))

	privileged := true
	debugPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "netshoot-privileged-",
			Namespace:    k.namespace,
			Labels:       map[string]string{"debug": "true"},
		},
		Spec: corev1.PodSpec{
			NodeName:      target.Spec.NodeName,
			RestartPolicy: corev1.RestartPolicyNever,
			HostPID:       true,
			Volumes: []corev1.Volume{
				{
					Name: "docker-sock",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/run/docker.sock",
							Type: newHostPathType(corev1.HostPathSocket),
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:            "debug",
					Image:           NetshootImage,
					Command:         []string{"sh", "-c", script},
					ImagePullPolicy: corev1.PullAlways,
					SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "docker-sock",
							MountPath: "/var/run/docker.sock",
						},
					},
				},
			},
		},
	}

	pod, err := k.clientset.CoreV1().Pods(k.namespace).Create(context.TODO(), debugPod, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create privileged debug pod: %w", err)
	}

	log.Printf("Privileged debug pod created: %s", pod.Name)
	return pod.Name, nil
}

func (k *KubernetesApiServiceImpl) CreateConcurrentTcpdumpCapturePod(targetPod string, containers []string, duration time.Duration) (string, error) {
	log.Printf("Creating concurrent tcpdump capture pod for: %s", targetPod)

	target, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get target pod: %w", err)
	}

	scriptBuilder := strings.Builder{}
	scriptBuilder.WriteString("#!/bin/sh\nset -e\nmkdir -p /captures\n")
	for _, container := range containers {
		dockerLabel := fmt.Sprintf("k8s_%s_%s", container, targetPod)
		scriptBuilder.WriteString(fmt.Sprintf("CID=$(docker ps --format '{{.ID}} {{.Names}}' | grep \"%s\" | awk '{print $1}')\n", dockerLabel))
		scriptBuilder.WriteString("if [ -n \"$CID\" ]; then\n")
		scriptBuilder.WriteString("  PID=$(docker inspect -f '{{.State.Pid}}' $CID)\n")
		scriptBuilder.WriteString(fmt.Sprintf("  nsenter --target $PID --net -- tcpdump -i any -s 0 -w /captures/%s.pcap &\n", container))
		scriptBuilder.WriteString("fi\n")
	}
	scriptBuilder.WriteString(fmt.Sprintf("sleep %d\n", int(duration.Seconds())))
	scriptBuilder.WriteString("killall tcpdump || true\necho 'Tcpdump capture completed.'\nsleep 300\n")

	privileged := true
	tcpdumpPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tcpdump-capture-",
			Namespace:    k.namespace,
			Labels:       map[string]string{"debug": "true"},
		},
		Spec: corev1.PodSpec{
			NodeName:      target.Spec.NodeName,
			RestartPolicy: corev1.RestartPolicyNever,
			HostPID:       true,
			Volumes: []corev1.Volume{
				{
					Name: "docker-sock",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/run/docker.sock",
							Type: newHostPathType(corev1.HostPathSocket),
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "tcpdump",
					Image: NetshootImage,
					Command: []string{
						"sh", "-c", scriptBuilder.String(),
					},
					ImagePullPolicy: corev1.PullAlways,
					SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "docker-sock",
							MountPath: "/var/run/docker.sock",
						},
					},
				},
			},
		},
	}

	pod, err := k.clientset.CoreV1().Pods(k.namespace).Create(context.TODO(), tcpdumpPod, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create tcpdump capture pod: %w", err)
	}

	log.Printf("Tcpdump capture pod created: %s", pod.Name)
	return pod.Name, nil
}

func (k *KubernetesApiServiceImpl) ExecuteCommandWithStderr(pod string, container string, command []string, stdout, stderr io.Writer) (int, error) {
	req := k.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(pod).
		Namespace(k.namespace).
		SubResource("exec").
		Param("container", container).
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "false")

	for _, arg := range command {
		req.Param("command", arg)
	}

	exec, err := remotecommand.NewSPDYExecutor(k.restConfig, "POST", req.URL())
	if err != nil {
		return 0, fmt.Errorf("failed to create executor: %w", err)
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
		Tty:    false,
	})

	if err != nil {
		return 1, fmt.Errorf("command execution failed: %w", err)
	}

	return 0, nil
}

func newHostPathType(t corev1.HostPathType) *corev1.HostPathType {
	return &t
}
