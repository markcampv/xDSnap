# xDSnap
xDSnap: A kubectl plugin to capture and organize Envoy configuration snapshots across Kubernetes pods for streamlined service mesh diagnostics.

# xDSnap

xDSnap is a tool designed to capture Envoy configuration snapshots and perform network traffic analysis in a Consul service mesh. It allows users to capture Envoy endpoint information and DEBUG logs periodically on Kubernetes pods and save them as `.tar.gz` archives. 

## Table of Contents

- [Features](#features)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Usage](#usage)
- [Example](#example)
- [Configuration](#configuration)
- [Feature Requests](#-feature-requests)

---

## Features

- **Capture Envoy Snapshots**: Periodically fetch data from Envoy admin endpoints, dataplane logs, and application logs.
- **Optional TCPDump Injection**: Inject an ephemeral container to run `tcpdump` and capture network traffic. 
- **Data Archival**: Save collected data as `.tar.gz` files for easier storage and transfer.

---

## Prerequisites

- Kubernetes cluster with Consul service mesh and Envoy sidecars configured.
- Go 1.18+ installed for building the project from source.
- `kubectl` CLI configured to access your Kubernetes cluster.
- Permissions to inject ephemeral containers in pods (required for TCPDump functionality).

---

## Installation
### Using Krew (Recommended)

[Krew](https://krew.sigs.k8s.io/) is the plugin manager for kubectl. To install xDSnap via Krew:


1. **Install Krew (if not already installed)**:

```bash
# On Linux or macOS
(
  set -x; cd "$(mktemp -d)" &&
  OS="$(uname | tr '[:upper:]' '[:lower:]')" &&
  ARCH="$(uname -m | sed -e 's/x86_64/amd64/' -e 's/arm.*$/arm/')" &&
  KREW="krew-${OS}_${ARCH}" &&
  curl -fsSLO "https://github.com/kubernetes-sigs/krew/releases/latest/download/${KREW}.tar.gz" &&
  tar zxvf "${KREW}.tar.gz" &&
  ./${KREW} install krew
)

# Add Krew to your PATH
export PATH="${KREW_ROOT:-$HOME/.krew}/bin:$PATH"
```

2. **Install xDSnap via Krew**:
    ```bash
   kubectl krew install xdsnap
    ```

3. **Verify installation**:
    ```bash
   kubectl xdsnap --help
    ```


---

## Usage

The main command to use xDSnap is `capture`, which collects snapshots from specified Envoy endpoints within a given namespace, pod, and container.

### Basic Command
```bash
kubectl xdsnap capture --namespace <namespace> --pod <pod-name> --container <container-name>
```

### Flags

- `--namespace`, `-n` : Namespace of the pod.
- `--pod` : Name of the target pod (optional; if omitted, captures all Consul-injected pods).
- `--container` : Name of the application container.
  Do **not** specify the `consul-dataplane` container—this will cause the tool to exit automatically, as exec'ing into the dataplane is not supported.
- `--sleep` : Interval between data captures (in seconds, default: 5).
- `--duration` : Duration to run the capture process (in seconds, default: 60).
- `--repeat` : Number of times to take a snapshot.
- `--enable-trace`: Temporarily set Envoy log level to trace during capture (auto-reverts to info afterward).
- `--tcpdump`: Enables tcpdump capture using a privileged ephemeral debug pod (only supports single-run capture).
- `--output-dir` : Directory to save the snapshots (default: current directory).
- `--endpoints` : Specific Envoy admin endpoints to capture (default: `["/stats", "/config_dump", "/listeners", "/clusters", "/certs"]`).

### Example

The following example captures data from the `static-client` container within the `static-client-685c8c98dd-r9wc5` pod in the `consul` namespace, for a duration of 60 seconds:

```bash
kubectl xdsnap capture --namespace consul --pod static-client-685c8c98dd-r9wc5 --container static-client  --duration 60
```
#### Enable verbose Envoy logs (trace) during capture

```bash
kubectl xdsnap capture --namespace consul --pod static-client-685c8c98dd-r9wc5 --container static-client  --duration 120 --enable-trace
```

#### Run exactly 3 snapshots

```bash
kubectl xdsnap capture --namespace consul --pod static-client-685c8c98dd-r9wc5 --container static-client  --repeat 3
```

#### Capture network traffic with tcpdump

```bash
kubectl xdsnap capture --namespace consul --pod dashboard-8bd546b69-m6v4q --container dashboard --tcpdump
```

#### Run trace logging and tcpdump together

```bash
kubectl xdsnap capture --namespace consul --pod dashboard-8bd546b69-m6v4q --container dashboard --enable-trace --tcpdump
```

#### Invalid: Targeting `consul-dataplane` as the container

```bash
kubectl xdsnap capture --namespace consul --pod dashboard-8bd546b69-m8knh --container consul-dataplane 
```

> ❌ **This will fail with the following error:**
>
> ```
> Error: 'consul-dataplane' cannot be used as the --container value. Please specify the application container instead
> ```
>
> You should specify the **application container**, such as `dashboard`, and `xDSnap` will automatically locate and interact with the sidecar (`consul-dataplane`) as needed.
### Configuration

#### Environment Variables
- **KUBECONFIG**: Specify the path to the Kubernetes configuration file if running outside a Kubernetes cluster.

#### Notes
- The tool attempts to use in-cluster configuration. If unsuccessful, it falls back to using `KUBECONFIG`.
- When `--tcpdump` is enabled, a temporary debug pod is created in the same network namespace to capture packet data. The resulting `.pcap` file is included in the final snapshot.
- `--repeat` controls the number of capture cycles. If set, it runs that many times. `--duration` can still be used alongside it to enforce a graceful timeout for the entire session.
- The tool automatically detects sidecar containers and selects the appropriate method (`wget` or a debug pod) to set the Envoy log level.
- You can use the application container for endpoint capture even if the dataplane sidecar is used to toggle log levels.

## 💡 Feature Requests

We welcome suggestions and ideas to improve xDSnap!

### 🙋 How Do I Submit a New Feature Request?

If you have an idea for a new feature, please [open a new issue](https://github.com/markcampv/xdsnap/issues/new?template=feature_request.md) using the **Feature Request** template. Make sure to provide the following:

- **Brief Description**  
  What is the feature you'd like to see added?

- **Use Case / Motivation**  
  How would you use this feature, and why is it important? What problem does it solve?

- **Proposed Changes**  
  Describe any anticipated changes to:
    - Command-line interface (CLI)
    - Output format or structure
    - Integration with other tools or services

- **Alternatives Considered**  
  Are there any current workarounds or existing tools you’ve tried?

- **Additional Context** *(Optional)*  
  Screenshots, logs, or sample outputs that illustrate the need or behavior you're requesting.

---

Your input helps shape the direction of xDSnap. We review all submissions and will provide feedback or updates when action is taken.

Thank you for contributing!
