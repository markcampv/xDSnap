package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	"log"
	"net/http"
	"strings"
	"time"
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
	PortForwardGET(pod string, podPort int, path string) ([]byte, error)
	RunEphemeralInTargetNetNS(targetPod, targetContainer string, command []string, privileged bool, timeout time.Duration) error
	RunEphemeralInTargetNetNSWithOutput(targetPod, targetContainer string, command []string, privileged bool, timeout time.Duration, stdout, stderr io.Writer) error
	StartEphemeralTcpdump(targetPod, targetContainer string, duration time.Duration, outPath string) error
	StartEphemeralTcpdumpToLogs(targetPod, targetContainer string, duration time.Duration) (string, error)
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

func (k *KubernetesApiServiceImpl) PortForwardGET(pod string, podPort int, path string) ([]byte, error) {
	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(k.namespace).
		Name(pod).
		SubResource("portforward")

	rt, upgrader, err := spdy.RoundTripperFor(k.restConfig)
	if err != nil {
		return nil, fmt.Errorf("roundtripper: %w", err)
	}

	stopCh, readyCh := make(chan struct{}, 1), make(chan struct{}, 1)

	// capture forwarder stderr to surface kubelet/apiserver errors
	var pfErrBuf bytes.Buffer

	fw, err := portforward.NewOnAddresses(
		spdy.NewDialer(upgrader, &http.Client{Transport: rt}, "POST", req.URL()),
		[]string{"127.0.0.1"},
		[]string{fmt.Sprintf("%d:%d", podPort, podPort)},
		stopCh, readyCh, io.Discard, &pfErrBuf,
	)
	if err != nil {
		return nil, fmt.Errorf("portforward ctor: %w", err)
	}

	// run the forwarder and watch for early exit
	done := make(chan error, 1)
	go func() { done <- fw.ForwardPorts() }()

	// wait for ready, error, or timeout
	select {
	case <-readyCh:
		// ok
	case err := <-done:
		close(stopCh)
		msg := strings.TrimSpace(pfErrBuf.String())
		if err == nil && msg != "" {
			return nil, fmt.Errorf("port-forward failed: %s", msg)
		}
		return nil, fmt.Errorf("port-forward exited early: %w", err)
	case <-time.After(12 * time.Second):
		close(stopCh)
		msg := strings.TrimSpace(pfErrBuf.String())
		if msg == "" {
			msg = "timeout waiting for port-forward readiness"
		}
		return nil, fmt.Errorf(msg)
	}

	// make the request
	url := fmt.Sprintf("http://127.0.0.1:%d%s", podPort, path)
	resp, err := http.Get(url)
	if err != nil {
		close(stopCh)
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	close(stopCh)
	if err != nil {
		return nil, fmt.Errorf("read resp: %w", err)
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("GET %s -> %s (%d): %s", path, resp.Status, resp.StatusCode, msg)
	}
	return b, nil
}

// RunEphemeralInTargetNetNS adds an ephemeral container to the target pod that joins
// the target container's namespaces (incl. netns) and runs `command`.
// It waits until the ephemeral container starts and then exits (or until timeout).
func (k *KubernetesApiServiceImpl) RunEphemeralInTargetNetNS(
	targetPod, targetContainer string,
	command []string,
	privileged bool,
	timeout time.Duration,
) error {

	if targetPod == "" || targetContainer == "" {
		return fmt.Errorf("targetPod and targetContainer are required")
	}
	if len(command) == 0 {
		return fmt.Errorf("command must not be empty")
	}

	// 1) Get current pod
	pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}

	// 2) Build the ephemeral container
	ecName := fmt.Sprintf("xdsnap-ephem-%d", time.Now().UnixNano())
	ec := corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:            ecName,
			Image:           NetshootImage, // already defined in your code
			Command:         command,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
		},
		TargetContainerName: targetContainer,
	}

	// 3) Append to a copy of the pod and update the subresource
	podCopy := pod.DeepCopy()
	podCopy.Spec.EphemeralContainers = append(podCopy.Spec.EphemeralContainers, ec)

	if _, err := k.clientset.CoreV1().
		Pods(k.namespace).
		UpdateEphemeralContainers(context.TODO(), targetPod, podCopy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update ephemeral containers: %w", err)
	}

	// 4) Wait for the ephemeral container to run and terminate
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("ephemeral container %q did not finish within %s", ecName, timeout)
		}

		cur, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var st *corev1.ContainerState
		for i := range cur.Status.EphemeralContainerStatuses {
			if cur.Status.EphemeralContainerStatuses[i].Name == ecName {
				st = &cur.Status.EphemeralContainerStatuses[i].State
				break
			}
		}

		if st == nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if st.Running != nil {
			// still running, keep polling
			time.Sleep(300 * time.Millisecond)
			continue
		}

		if st.Terminated != nil {
			// success — consider exit code if you want stricter checks
			return nil
		}

		// Waiting state
		time.Sleep(500 * time.Millisecond)
	}
}

// RunEphemeralInTargetNetNSWithOutput runs a command inside an ephemeral
// container that shares the netns of targetContainer. Captures stdout/stderr
// if writers are provided. Waits until the container finishes or timeout.
func (k *KubernetesApiServiceImpl) RunEphemeralInTargetNetNSWithOutput(
	targetPod, targetContainer string,
	command []string,
	privileged bool,
	timeout time.Duration,
	stdout, stderr io.Writer,
) error {

	if targetPod == "" || targetContainer == "" {
		return fmt.Errorf("targetPod and targetContainer are required")
	}
	if len(command) == 0 {
		return fmt.Errorf("command must not be empty")
	}

	// 1. Fetch pod
	pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}

	// 2. Define ephemeral container
	ecName := fmt.Sprintf("xdsnap-ephem-%d", time.Now().UnixNano())
	ec := corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:            ecName,
			Image:           NetshootImage,
			Command:         command,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
		},
		TargetContainerName: targetContainer,
	}

	// 3. Patch ephemeral containers
	podCopy := pod.DeepCopy()
	podCopy.Spec.EphemeralContainers = append(podCopy.Spec.EphemeralContainers, ec)

	if _, err := k.clientset.CoreV1().
		Pods(k.namespace).
		UpdateEphemeralContainers(context.TODO(), targetPod, podCopy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update ephemeral containers: %w", err)
	}

	// 4. Wait for container to terminate and fetch logs
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("ephemeral container %q did not finish within %s", ecName, timeout)
		}

		cur, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var st *corev1.ContainerState
		for i := range cur.Status.EphemeralContainerStatuses {
			if cur.Status.EphemeralContainerStatuses[i].Name == ecName {
				st = &cur.Status.EphemeralContainerStatuses[i].State
				break
			}
		}

		if st == nil || st.Running != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if st.Terminated != nil {
			// fetch logs
			req := k.clientset.CoreV1().Pods(k.namespace).GetLogs(targetPod, &corev1.PodLogOptions{
				Container: ecName,
			})
			stream, err := req.Stream(context.TODO())
			if err != nil {
				return fmt.Errorf("logs: %w", err)
			}
			defer stream.Close()

			if stdout != nil {
				if _, err := io.Copy(stdout, stream); err != nil {
					return fmt.Errorf("copy logs to stdout: %w", err)
				}
			} else {
				io.Copy(io.Discard, stream)
			}
			return nil
		}

		time.Sleep(400 * time.Millisecond)
	}
}

// StartEphemeralTcpdump runs tcpdump inside the target pod's netns and writes a single file.
func (k *KubernetesApiServiceImpl) StartEphemeralTcpdump(
	targetPod, targetContainer string,
	duration time.Duration,
	outPath string,
) error {
	if outPath == "" {
		outPath = "/tmp/xdsnap.pcap"
	}
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf("timeout %ds tcpdump -i any -s0 -w %s || true", int(duration.Seconds()), outPath),
	}
	// tcpdump often needs CAP_NET_RAW/ADMIN — simplest is privileged=true for the ephemeral ctr.
	return k.RunEphemeralInTargetNetNS(targetPod, targetContainer, cmd, true, duration+5*time.Second)
}

func (k *KubernetesApiServiceImpl) StartEphemeralTcpdumpToLogs(
	targetPod, targetContainer string,
	duration time.Duration,
) (string, error) {

	if targetPod == "" || targetContainer == "" {
		return "", fmt.Errorf("targetPod and targetContainer are required")
	}

	// Build the ephemeral container that streams tcpdump to stdout as base64.
	// -U : packet-buffered (flush on packet), -s0 : full snaplen, -w - : write to stdout
	ecName := fmt.Sprintf("xdsnap-tcpdump-%d", time.Now().UnixNano())
	cmd := []string{
		"sh", "-c",
		// tcpdump -> write pcap to stdout; drop stderr noise; base64 encode; strip newlines; never fail the pipe.
		fmt.Sprintf("timeout %ds tcpdump -i any -s0 -U -w - 2>/dev/null | base64 | tr -d '\\n\\r' || true", int(duration.Seconds())),
	}

	priv := true

	// Fetch pod and append ephemeral container
	pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get pod: %w", err)
	}

	ec := corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:            ecName,
			Image:           NetshootImage,
			Command:         cmd,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{Privileged: &priv},
		},
		TargetContainerName: targetContainer,
	}

	podCopy := pod.DeepCopy()
	podCopy.Spec.EphemeralContainers = append(podCopy.Spec.EphemeralContainers, ec)

	if _, err := k.clientset.CoreV1().
		Pods(k.namespace).
		UpdateEphemeralContainers(context.TODO(), targetPod, podCopy, metav1.UpdateOptions{}); err != nil {
		// Surface lack of RBAC clearly to callers
		if apierrors.IsForbidden(err) {
			return "", fmt.Errorf("rbac: update pods/ephemeralcontainers forbidden: %w", err)
		}
		return "", fmt.Errorf("update ephemeral containers: %w", err)
	}

	// Wait until the ephem container appears and then terminates (the timeout is implicit in tcpdump command)
	deadline := time.Now().Add(duration + 60*time.Second) // allow image pull / spin-up slack
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("ephemeral container %q did not finish within %s", ecName, duration+60*time.Second)
		}
		cur, err := k.clientset.CoreV1().Pods(k.namespace).Get(context.TODO(), targetPod, metav1.GetOptions{})
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var st *corev1.ContainerState
		for i := range cur.Status.EphemeralContainerStatuses {
			if cur.Status.EphemeralContainerStatuses[i].Name == ecName {
				st = &cur.Status.EphemeralContainerStatuses[i].State
				break
			}
		}
		if st == nil || st.Running != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if st.Terminated != nil {
			// Done; logs are now available to read from the ephemeral container by name.
			return ecName, nil
		}
		time.Sleep(400 * time.Millisecond)
	}
}

func (k *KubernetesApiServiceImpl) CreatePrivilegedDebugPod(targetPod string, containerName string, command []string) (string, error) {
	if len(command) == 0 {
		return "", fmt.Errorf("no command specified")
	}
	if err := k.RunEphemeralInTargetNetNS(targetPod, containerName, command, true, 10*time.Second); err != nil {
		return "", err
	}
	// Return a synthetic name so existing callers don't break.
	return "ephemeral-" + targetPod, nil
}

func (k *KubernetesApiServiceImpl) CreateConcurrentTcpdumpCapturePod(targetPod string, containers []string, duration time.Duration) (string, error) {
	// pick likely dataplane/envoy/gateway target; fall back to first
	candidates := []string{"consul-dataplane", "envoy-sidecar", "mesh-gateway", "api-gateway"}

	var targetContainer string
	for _, c := range candidates {
		if ok, _ := k.ContainerExists(targetPod, c); ok {
			targetContainer = c
			break
		}
	}

	if targetContainer == "" {
		if len(containers) > 0 {
			targetContainer = containers[0]
		} else {
			return "", fmt.Errorf("no suitable containers found in pod %s", targetPod)
		}
	}

	// Launch ephemeral tcpdump that streams to logs; return the ephemeral container name
	ecName, err := k.StartEphemeralTcpdumpToLogs(targetPod, targetContainer, duration)
	if err != nil {
		return "", err
	}
	return ecName, nil
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
