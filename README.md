# xDSnap
xDSnap: A kubectl plugin to capture and organize Envoy configuration snapshots across Kubernetes pods for streamlined service mesh diagnostics.

# xDSnap

xDSnap is a tool designed to capture Envoy configuration snapshots and perform network traffic analysis in a Consul service mesh. It allows users to capture endpoint information periodically from Envoy's admin endpoints on Kubernetes pods and save them as `.tar.gz` archives. 

## Table of Contents

- [Features](#features)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Usage](#usage)
- [Commands](#commands)
- [Example](#example)
- [Configuration](#configuration)

---

## Features

- **Capture Envoy Snapshots**: Periodically fetch data from Envoy admin endpoints.
- (WIP)**Optional TCPDump Injection**: Inject an ephemeral container to run `tcpdump` and capture network traffic. 
- **Data Archival**: Save collected data as `.tar.gz` files for easier storage and transfer.

---

## Prerequisites

- Kubernetes cluster with Consul service mesh and Envoy sidecars configured.
- Go 1.18+ installed for building the project from source.
- `kubectl` CLI configured to access your Kubernetes cluster.
- Permissions to inject ephemeral containers in pods (required for TCPDump functionality).

---

## Installation

1. **Clone the repository**:
    ```bash
    git clone https://github.com/markcampv/xDSnap.git
    cd xDSnap
    ```

2. **Build the project**:
    ```bash
    go build -o xdsnap ./cmd
    ```

3. **Verify the executable**:
    ```bash
    ./xdsnap --help
    ```

---

## Usage

The main command to use xDSnap is `capture`, which collects snapshots from specified Envoy endpoints within a given namespace, pod, and container.

### Basic Command
```bash
./xdsnap capture --namespace <namespace> --pod <pod-name> --container <container-name>
```

### Flags

- `--namespace`, `-n` : Namespace of the pod.
- `--pod` : Name of the target pod (optional; if omitted, captures all Consul-injected pods).
- `--container` : Name of the container running Envoy.
- `--interval` : Interval between data captures (in seconds, default: 30).
- `--duration` : Duration to run the capture process (in seconds, default: 60).
- `--output-dir` : Directory to save the snapshots (default: current directory).
- `--endpoints` : Specific Envoy admin endpoints to capture (default: `["/stats", "/config_dump", "/listeners", "/clusters"]`).

### Example

The following example captures data from the `static-client` container within the `static-client-685c8c98dd-r9wc5` pod in the `consul` namespace, every 30 seconds for a duration of 60 seconds:

```bash
./xdsnap capture --namespace consul --pod static-client-685c8c98dd-r9wc5 --container static-client --interval 30 --duration 60
```


### Configuration

#### Environment Variables
- **KUBECONFIG**: Specify the path to the Kubernetes configuration file if running outside a Kubernetes cluster.

#### Notes
- The tool attempts to use in-cluster configuration. If unsuccessful, it falls back to using `KUBECONFIG`.
