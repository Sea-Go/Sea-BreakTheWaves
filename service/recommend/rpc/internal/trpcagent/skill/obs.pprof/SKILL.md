---
name: obs.pprof
category: obs
description: pprof 性能分析
tools:
  - pprof_capture
  - pprof_analyze
inputs:
  - name: profile_type
    type: string
    required: true
    description: profile 类型（cpu/heap/goroutine/block/mutex）
  - name: duration
    type: int
    required: false
    default: 30
    description: 采样时长（秒，cpu profile 用）
  - name: analyze
    type: bool
    required: false
    default: true
    description: 是否自动分析 top 函数
outputs:
  - name: profile_path
    type: string
    description: profile 文件路径
  - name: top_funcs
    type: list
    description: top 函数列表（含 cpu/内存占比）
  - name: summary
    type: map
    description: 分析摘要
---

# obs.pprof

## 用途
对推荐服务做 Go pprof 性能采集与分析。底层走标准 `net/http/pprof` 端点。用于：定位 CPU 热点（哪个 recall / rank 函数最烧 CPU）、内存泄漏（哪个对象常驻不释放）、goroutine 阻塞（哪个锁 / 通道卡住）、线上延迟尖刺归因。

## 调用方式
Agent 通过 tool_call 调用，传入 `profile_type` / `duration`。底层流程：
1. 调 `pprof_capture` 从服务 pprof 端点采 profile（cpu 需 `duration` 秒，其余即时）；
2. 落盘到 `profile_path`；
3. 若 `analyze=true`，调 `pprof_analyze` 跑 `go tool pprof -top`，提取 top 函数；
4. 返回 `profile_path` / `top_funcs` / `summary`。

注意：cpu profile 会让服务有轻微开销，生产环境控制采样时长与频率。

## 二开扩展点
- 接入持续 profiling（定时采短样本，做长期趋势）
- 自定义分析报告（生成火焰图 SVG）
- 与 `obs.trace` 联动（trace 发现延迟尖刺时自动触发 pprof）
- 多实例对比（对比不同实例的 profile 找异常节点）
