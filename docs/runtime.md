# 共享运行与提供者客户端

## 工作区域

- [W0:ROOT] `Sea-BreakTheWaves` 的 `feat/runtime-content-20260914` 隔离工作树。
- [W1:WRITE] 根 `go.mod/go.sum`、`internal/runtime/`、`internal/clients/datacenter/`、`internal/clients/ridethewind/`、本文。
- [R1:READ_ONLY] Docs 任务契约、DC 提供者公开 contracts 与 RTW `api/knowledge.api`；content/corpus 由集成 writer 维护。
- [D1:DEPENDENCY] 固定版本 tRPC-Agent-Go 核心与 PostgreSQL session 子模块源码。
- [G1:GENERATED] 客户端类型快照由提供者公开契约生成。
- [X1:EXTERNAL] 仅任务专用、回环地址隔离 PostgreSQL/HTTP 可写，生产不在范围。
- [N1:OUT_OF_SCOPE] 存量 recommendation、agent_v2/v3 和其他原工作树。
- [T1:TEMP] 系统任务临时目录，存放验收服务和日志。

主职责 [C6:INFRA]；跨区 [C5:AGENT_ADAPTER] tRPC Runner/Session/Model，[C7:CONTRACT] H03/H04/H05 提供者 HTTP 与固定生成类型，[C8:VERIFY] 运行、取消、恢复证据。客户端不承接产品发布或领域接纳状态。

## 版本

根模块为 `github.com/Sea-Go/Sea-BreakTheWaves`；复用存量锁定的 tRPC-Agent-Go `v1.8.1`。PostgreSQL session 独立子模块没有 `v1.8.1` 标签，选同系列 `v1.8.0` 并核对兼容性。新增根模块是最终工程装配入口，既有子模块暂保留在自身边界。

## 验收状态

实施中；未完成真实模型、产品首引收据及全 WS06-G 验收。
