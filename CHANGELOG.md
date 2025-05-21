# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org).

Generated manually. For contributions, please update this file when submitting PRs.

## [Unreleased]

### Added
- Tcpdump integration planning using `ksniff`.

### Changed
- Restructured CLI layout under `cmd/`.
- Improved resource efficiency by minimizing container overhead during snapshot.
- Replaced `wget` with `curl` in admin API interaction for better reliability.

### Fixed
- Log level was not being reverted in edge cases — now restored post-capture.

## [0.2.8] - 2025-05-19

### Added
- New support for capturing tcpdump traffic from pods using ephemeral privileged debug containers (similar to `ksniff`).
- `.pcap` files from tcpdump are now included in the snapshot tarball when `--tcpdump-enabled` is specified.
- Support for running a single snapshot cycle (no interval loop) when tcpdump capture is active.
- Automatic sidecar detection now determines whether to use `wget` or a debug pod for interacting with the Envoy admin API.
- Application container can now be used for endpoint capture even if dataplane is used for log toggling.

### Changed
- Tcpdump capture and Envoy config fetch now run concurrently across targeted pods.
- Logging behavior has been refined for clarity when combining application, dataplane, and tcpdump captures.
- Ephemeral debug pod creation uses a consistent naming convention and ensures proper cleanup after capture.

### Fixed
- Resolved an issue where log level toggling would silently fail if `consul-dataplane` was not the target container.
- Fixed bug where `--container` value defaulting logic failed when missing or misused in multi-container pods.


## [0.2.7] - 2025-05-06

### Added.
- Initial support for installing xDSnap via [Krew](https://krew.sigs.k8s.io), the kubectl plugin manager.


## [0.2.2] - 2025-05-05

### Added
- Logs are now fetched from both application and dataplane containers and stored in the snapshot directory.
- Log collection runs in parallel with Envoy xDS data capture for efficiency.
- Envoy log level is now automatically toggled to `debug` at the start of capture and reverted to `info` afterward.
- Snapshot output now includes additional metadata for improved traceability.
- Captures output from the Envoy `/certs` endpoint for TLS insight.


### Changed
- The `--duration` flag is now used to control how long live logging should run.

### Fixed
- Fixed an issue where snapshot capture could hang due to missing context timeout — timeouts are now properly implemented.



## [0.1.0] - 2024-11-09

### Added
- Initial release of xDSnap.
- Captures Envoy xDS configuration via `/config_dump`.
- Supports targeting a single pod or all mesh-connected pods with Connect injection.
- Outputs snapshots in organized folder structure.





