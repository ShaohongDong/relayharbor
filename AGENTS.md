# Repository Guidelines

## Structure and Toolchain

This is a Go lifecycle manager and unprivileged scheduler for independent, verified GOST v3 processes. `cmd/gostctl` is the production entrypoint; `internal/` contains configuration, native GOST generation, SOCKS protocol, supervision, lifecycle, API and CLI packages. `cmd/testctl` and `internal/testkit` are test-only utilities. `tests/integration` contains systemd acceptance tests; `examples/` and `docs/` document public contracts.

Use Go 1.27.2. Production Go code must use the standard library and build with `CGO_ENABLED=0` for Linux amd64/arm64. `web/` contains the Vue 3 / TypeScript console, built with Node.js 24 LTS and npm lockfile dependencies; generated assets are embedded into Go and ignored by Git. Target hosts require neither Node.js, Go nor Python. Only management config schemaVersion 2 and Go installation state are supported; do not add Python deployment migration.

## Commands and Style

- `make build`: compile `bin/gostctl` and its checksum manifest.
- `make web-build` / `make web-test` / `make web-e2e`: build embedded frontend assets, run frontend unit tests and browser tests. Run `make web-build` before standalone Go commands that import the API.
- `make check`: check gofmt, go vet, Bash syntax and whitespace.
- `make test` / `make test-race`: Go policy, API, transaction and protocol checks. Real protocol tests require verified GOST archives (`make fetch`).
- `make integration`: five isolated systemd environments plus two ARM64 QEMU protocol environments; needs Docker and qemu-aarch64-static.
- `make release VERSION=...`: create Linux amd64/arm64 tarballs and checksum manifests without publishing.

Use gofmt, idiomatic Go naming, explicit contexts and bounded network operations. Bash scripts retain `set -euo pipefail`, quoted expansions and safe arguments. Add behavior-focused tests for public contract, transaction and concurrency changes.

## Security and Validation

Preserve mandatory SOCKS authentication, release checksum verification, fail-closed pools, shared entrypoint and acknowledged connection-preserving profile changes. Never commit credentials or generated `bin/`, `dist/`, `.cache/` or `test-results/`. Do not edit installed state directly; use candidate configs and configure.

Run privileged tests only against trusted code inside isolated containers. Never mount host `/etc`, `/run` or cgroup paths. ARM64 QEMU protocol checks are not hardware/systemd acceptance. Record source-bound evidence and state limitations honestly.

## Git

Use concise action-oriented commit subjects. Commit or push only when requested; stage explicit related paths and preserve unrelated work. Describe behavior, validation and deployment implications in review descriptions.
