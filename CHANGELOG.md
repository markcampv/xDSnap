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

## [0.2.2] - 2025-05-05

### Added
- Logs are now fetched from both application and dataplane containers and stored in the snapshot directory.
- Log collection runs in parallel with Envoy xDS data capture for efficiency.
- Envoy log level is now automatically toggled to `debug` at the start of capture and reverted to `info` afterward.
- Snapshot output now includes additional metadata for improved traceability.

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





