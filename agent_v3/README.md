# agent_v3 - Travel Planning Agent

`agent_v3` is the refactored version of the travel-planning agent. It keeps the runnable behavior of `agent_v2`, while reorganizing the code around the domain workflow so the project is easier to maintain, explain, and present.

## What Changed

- `agent_v2` remains untouched as the working baseline.
- Runtime code now lives under `internal/`, split by responsibility instead of by one large `agent/` package.
- Tool implementations are separated by capability:
  - `internal/tools/amap`
  - `internal/tools/graph`
  - `internal/tools/zhihu`
  - `internal/tools/bilibili`
- Server startup is centralized in `internal/app/bootstrap`.
- HTTP delivery entrypoints are grouped under `internal/app/delivery`.
- Workflow runtime, orchestration, stages, trace emission, and review logic are split into dedicated packages under `internal/workflow` and `internal/review`.
- The root `go run .` entrypoint is now a thin wrapper over the bootstrap package.
- `cmd/server` is available as the explicit server command for demos and deployment.
- Architecture notes and retained file guides live under `docs/`. Generated outputs and local live-check reports are kept out of version control.

## Project Layout

```text
agent_v3/
  main.go                         # Thin root entrypoint: go run .
  cmd/
    server/                       # Explicit server entrypoint
    run_travel_agent/             # CLI runner
    amap_live_check/              # Amap tool live check
    zhihu_live_check/             # Zhihu tool live check
    bilibili_live_check/          # Bilibili tool live check

  internal/
    app/
      bootstrap/                  # Config, telemetry, graph, and HTTP server wiring
      delivery/
        agui/                     # AG-UI handler adapter
        stream/                   # Travel SSE stream adapter

    agents/
      travel/                     # Travel agent assembly and adapter factories
      amap/                       # Amap-specific agent runtime
      dili360/                    # Dili360-specific agent runtime
      modelrouter/                # Shared model routing and usage tracking

    review/                       # Review agents and review execution helpers

    workflow/
      runtime/                    # Workflow stage constants and runtime state
      orchestrator/               # Requirement intake/merge orchestration
      stages/                     # Graph workflow runner and stage executors
      trace/                      # Public planning event and map trace emission

    tools/
      amap/                       # Amap API tools
      graph/                      # Neo4j graph tools exposed to agents
      zhihu/                      # Zhihu guide material tools
      bilibili/                   # Bilibili guide material tools

    graph/                        # Neo4j client, schema, models, repositories
    config/                       # YAML config loading and defaults
    domain/                       # Pure travel-planning domain rules and schemas
    history/                      # Travel run history recording

  skills/                         # Skill definitions and scripts
  docs/                           # Architecture notes and retained file guides
  examples/                       # Reserved for demo inputs and examples
```

## Workflow

The planning flow remains behavior-compatible with `agent_v2`:

```text
requirement_intake
  -> requirement_merge
  -> macro_planning
  -> graph_splitting
  -> day_expansion
  -> review
  -> final_output
```

`internal/agents/travel` is now mostly a thin assembly layer. The graph workflow runner lives under `internal/workflow/stages`, while intake/merge orchestration, runtime state, review execution, and public planning trace emission have been extracted into their own packages.

## Run

```bash
go run .
```

or:

```bash
go run ./cmd/server
```

The server listens on:

- `http://127.0.0.1:8088/agui`
- `http://127.0.0.1:8088/travel/stream`

## Test And Build

```bash
go test ./...
go build ./cmd/server
go build ./cmd/run_travel_agent
go build ./cmd/amap_live_check
go build ./cmd/zhihu_live_check
go build ./cmd/bilibili_live_check
```

Live-check commands require local API configuration. Compilation and unit tests do not require live API access.

## Configuration

Copy the example config and fill in local credentials:

```bash
cp config.example.yaml config.yaml
```

The config schema is intentionally kept compatible with `agent_v2`.
