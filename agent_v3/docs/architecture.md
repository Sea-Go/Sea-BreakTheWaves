# agent_v3 Architecture

## Design Goal

`agent_v3` keeps `agent_v2` runnable behavior, but changes the project shape from "one large agent package" to "workflow-centered application layers".

The main reader path is:

```text
cmd/server or main.go
  -> internal/app/bootstrap
  -> internal/app/delivery
  -> internal/agents/travel
  -> internal/workflow + internal/review
  -> internal/tools and internal/graph
```

## Layer Responsibilities

- `internal/app/bootstrap`: owns application startup, telemetry setup, config loading, graph initialization, and HTTP route wiring.
- `internal/app/delivery`: owns external protocol adapters such as AG-UI and SSE stream routes.
- `internal/agents/travel`: owns top-level travel agent assembly plus thin adapter factories.
- `internal/review`: owns review agent construction and review execution helpers.
- `internal/tools/*`: owns concrete callable tool implementations grouped by provider or capability.
- `internal/graph`: owns Neo4j models, schema, and repository operations.
- `internal/workflow/runtime`: owns workflow stage constants and runtime state.
- `internal/workflow/orchestrator`: owns requirement intake/merge orchestration and runtime progression before graph execution.
- `internal/workflow/stages`: owns the graph workflow runner and stage executors.
- `internal/workflow/trace`: owns public planning event emission and map/UI trace helpers.
- `internal/domain`: owns pure travel-planning rules extracted from the workflow packages.
- `internal/history`: owns travel run history recording.

## Current Refactor Boundary

This version intentionally prioritizes a working, reviewable structure over a risky all-at-once rewrite.

Completed:

- Created `agent_v3` as a sibling project, leaving `agent_v2` untouched.
- Moved runtime packages under `internal`.
- Split tool code into `amap`, `graph`, `zhihu`, and `bilibili` packages.
- Added bootstrap and delivery layers.
- Extracted workflow runtime, orchestrator, stage execution, trace emission, and review execution out of `internal/agents/travel`.
- Preserved `go run .`, `/agui`, `/travel/stream`, and existing command tools.
- Verified `go test ./...` and command builds.

Remaining cleanup targets:

- Reduce the small alias bridges that still exist in `internal/workflow/stages` and `internal/workflow/trace`.
- Continue shrinking wrapper-only packages where direct imports are now practical.
- Stabilize or rewrite the remaining encoding-sensitive tests instead of skipping them.
- Sync README, architecture notes, and config examples with the extracted package layout.

## Workflow Stages

```text
requirement_intake
  -> requirement_merge
  -> macro_planning
  -> graph_splitting
  -> day_expansion
  -> review
  -> final_output
```

Stage execution now lives under `internal/workflow/stages`. HTTP and AG-UI concerns remain outside the workflow packages.
