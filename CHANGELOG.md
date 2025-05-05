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

## [0.2.1] - 2025-05-05

### Added
- Support for interval-based xDS snapshots (`--interval` flag).
- Option to target all Connect-injected pods or a specific pod by name.
- Organized output directories by pod name and timestamp for better traceability.

### Changed
- Replaced use of Consul CLI with direct Envoy admin API calls for log level management.
- Improved error handling when target pod is not reachable or doesn't have a dataplane container.

### Fixed
- Log level now always correctly reverts, even on capture failure.
- Fixed occasional race when capturing multiple pods in parallel.

## [0.1.0] - 2024-11-09

### Added
- Initial release of xDSnap.
- Captures Envoy xDS configuration via `/config_dump`.
- Supports targeting a single pod or all mesh-connected pods with Connect injection.
- Outputs snapshots in organized folder structure.



