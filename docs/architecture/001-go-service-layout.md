# ADR-001: Organize the Go service with `cmd/` and `internal/`

**Status:** Accepted

**Date:** 2026-09-27

**Deciders:** FrameVault maintainers

## Context

FrameVault is a self-contained Go HTTP service with an embedded dashboard, SQLite state, background processing, and a separately deployed Modal worker. Its Go implementation and tests previously sat in the repository root alongside container configuration, documentation, browser assets, and Python code. The refactor should make the service layout easier to navigate while preserving executable behavior, embedded URL paths, the deployment image and binary name, the data schema, and provider workflows.

The Go project guidance recommends keeping server implementation packages in `internal/` and collecting command entrypoints under `cmd/`, particularly when a server repository also contains non-Go files ([Organizing a Go module](https://go.dev/doc/modules/layout)).

## Decision

Place the executable wrapper at `cmd/video-automation/main.go`. Keep the backend implementation and Go tests together in one cohesive `internal/app` package; its `Run()` function is the entrypoint called by the command. Move the embedded dashboard to `internal/webui/`, with `embed.go` beside `static/`, retaining the existing `/static/...` URLs. Place the separately deployed Python worker and its regression check under `workers/modal/`. Keep the module definition, Docker configuration, README, and operator documentation at the root or under `docs/` as appropriate.

Keep `internal/app` as a single package while routes, providers, processor, storage, and security share application types and lifecycle. This avoids exported cross-package plumbing that exists only to split files. Introduce more private packages when stable dependency boundaries appear; do not make implementation details public to external modules.

## Options Considered

### Option A: Command wrapper plus cohesive private application package — selected

| Dimension | Assessment |
|-----------|------------|
| Complexity | Low for this behavior-preserving move |
| Cost | No new runtime service or dependency |
| Scalability | Supports later private packages or additional commands |
| Team familiarity | Follows the Go server layout guidance |

**Pros:** Separates the executable from service implementation; keeps internals private; groups Go files away from non-Go assets; preserves the current package's type and test relationships.

**Cons:** `internal/app` remains broad until genuine package boundaries emerge.

### Option B: Keep all Go code at the repository root

| Dimension | Assessment |
|-----------|------------|
| Complexity | Lowest immediate file-move effort |
| Cost | No runtime cost |
| Scalability | Root becomes harder to navigate as assets and support files grow |
| Team familiarity | Valid for a small single-package Go module |

**Pros:** Avoids package relocation and import edits.

**Cons:** Keeps application code mixed with worker, dashboard assets, and operational files; provides no clear command boundary.

### Option C: Split the backend immediately into route, provider, processor, storage, and security packages

| Dimension | Assessment |
|-----------|------------|
| Complexity | High because the current code shares package-level types and lifecycle details |
| Cost | No direct runtime cost, but more maintenance and adaptation work |
| Scalability | Adds package boundaries if their contracts remain stable |
| Team familiarity | Common modular design, but boundaries need ongoing ownership |

**Pros:** Can isolate responsibilities behind explicit APIs.

**Cons:** Requires exported or adapter APIs before dependency boundaries are established, expanding a behavior-preserving refactor's risk and scope.

## Trade-off Analysis

The selected layout improves navigation and creates an explicit executable boundary while preserving existing backend package relationships. A single private application package is intentional because this change reorganizes files rather than service behavior. Future extraction should follow observed dependency boundaries rather than treating each existing file as a package.

## Consequences

- Build the command with `go build ./cmd/video-automation` and run it locally with `go run ./cmd/video-automation`.
- External modules cannot import `internal/app`; the command-facing `Run()` function is the intended cross-package entrypoint.
- Dashboard assets move beside their embedding source and keep the same browser URLs.
- The container build target changes while preserving `/app/video-automation`, port 8080, environment settings, storage paths, and health checks.
- Modal deployment and callback regression-check paths change to `workers/modal/`.
- Go package, HTTP, storage, security, provider, and workflow tests remain with `internal/app`.

## Action Items

1. [x] Move the Go command, application files, tests, embedded assets, and Modal worker into the selected layout.
2. [x] Update container and documented native, recovery, worker, and verification commands.
3. [x] Verify the refactor with Go tests and build, browser-script syntax check, Modal callback checks, and whitespace validation.
