---
name: eval.run
category: eval
description: 运行评估
tools:
  - eval_run
  - metric_compute
  - eval_report_render
inputs:
  - name: dataset
    type: string
    required: true
    description: 评估数据集名（testdata/baseline 或自定义）
  - name: metrics
    type: list
    required: false
    default: [ndcg, recall, ctr, coverage, diversity]
    description: 评估指标列表
  - name: channel
    type: string
    required: false
    description: 限定频道评估
  - name: topk
    type: int
    required: false
    default: 20
    description: 评估截断位置
outputs:
  - name: report
    type: map
    description: 评估报告（含各指标值 / 分组结果）
  - name: case_results
    type: list
    description: 每条 case 的明细结果
---

# eval.run

## 用途
对推荐系统跑一次离线评估，输出多指标报告。底层对应 `service/reco_evaluation_service.go` 与 `cmd/recrecorder`，使用 `testdata/baseline` 的标准 case 集。用于：上线前回归、策略调整后对比、A/B 实验离线预评。

## 调用方式
Agent 通过 tool_call 调用，传入 `dataset` 与 `metrics`。底层流程：
1. 加载评估数据集（baseline case 集或自定义 case）；
2. 对每条 case 跑完整推荐链路（召回→排序→重排），收集输出；
3. 调 `metric_compute` 计算 ndcg/recall/ctr/coverage/diversity；
4. 调 `eval_report_render` 渲染报告（含总体 + 分组 + 逐 case 明细）；
5. 返回 `report` 与 `case_results`。

可通过 `channel` 限定只评估某频道。

## 二开扩展点
- 新增评估指标（实现 `eval.Metric` 接口）
- 自定义评估数据集（按业务场景构造 case）
- 接入线上流量回放（用真实请求做评估）
- 评估报告推送（生成后自动发飞书 / 邮件）
