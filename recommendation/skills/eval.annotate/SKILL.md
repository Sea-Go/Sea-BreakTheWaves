---
name: eval.annotate
category: eval
description: 人工标注
tools:
  - annotate_task_create
  - annotate_result_ingest
  - annotation_quality_check
inputs:
  - name: task_type
    type: string
    required: true
    description: 标注任务类型（relevance/diversity/freshness/explanation_quality）
  - name: samples
    type: list
    required: true
    description: 待标注样本（user_id×article_id 对）
  - name: guidelines
    type: string
    required: false
    description: 标注指南文本
  - name: assignee
    type: string
    required: false
    description: 指定标注人
outputs:
  - name: task_id
    type: string
    description: 标注任务 ID
  - name: sample_count
    type: int
    description: 待标注样本数
  - name: status
    type: string
    description: 任务状态（pending/in_progress/completed）
---

# eval.annotate

## 用途
创建人工标注任务，把推荐结果交给标注员评判，产出高质量 ground truth。用于：补评估数据集、训练精排模型、校验 LLM 解释质量。底层对应 `internal/eval` 的标注流程与 `storage/reco_evaluation_repo`。

## 调用方式
Agent 通过 tool_call 调用，传入 `task_type` / `samples`。底层流程：
1. 调 `annotate_task_create` 建任务，分配 `task_id`；
2. 把 `samples` 拆成单条标注单元，写入标注队列；
3. 标注员在标注台完成后调 `annotate_result_ingest` 回灌；
4. 调 `annotation_quality_check` 做一致性校验（多人标注求 Kappa）；
5. 返回 `task_id` / `sample_count` / `status`。

任务完成后结果会进 `reco_evaluation_repo`，供 `eval.run` / `eval.regression` 引用。

## 二开扩展点
- 新增标注任务类型（如"内容安全"/"广告识别"）
- 接入外部标注平台（导出样本 / 导入结果）
- 标注质量控制（加黄金集探针，自动剔除低质标注员）
- 主动学习选样（优先标注模型最不确定的样本）
