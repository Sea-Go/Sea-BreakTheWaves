---
name: graph.generate_cypher
category: graph
description: 自然语言生成 Cypher
tools:
  - llm_generate_cypher
  - cypher_validate
inputs:
  - name: question
    type: string
    required: true
    description: 自然语言查询意图
  - name: schema_hint
    type: string
    required: false
    description: 图谱 schema 提示（节点/关系类型摘要）
  - name: examples
    type: list
    required: false
    description: Few-shot 示例（question/cypher 对）
outputs:
  - name: cypher
    type: string
    description: 生成的参数化 Cypher 语句
  - name: params
    type: map
    description: Cypher 参数绑定键值对
  - name: valid
    type: bool
    description: 是否通过语法/安全校验
---

# graph.generate_cypher

## 用途
把 Agent 的自然语言查询意图翻译成参数化 Cypher。用于让 LLM 直接驱动图谱查询，避免业务方手写 Cypher。该 Skill 是 `graph.query` 的"前置翻译层"，二者常组合使用：先 generate，再 execute。

## 调用方式
Agent 通过 tool_call 调用，传入 `question` 与可选的 `schema_hint` / `examples`。底层流程：
1. 组装 prompt（schema + few-shot + question）调用 LLM；
2. 解析 LLM 输出为 `cypher` + `params`；
3. 调 `cypher_validate` 做语法 + 关键字白名单校验（禁止写操作、禁止全图扫描）；
4. 返回 `valid=true` 的语句才允许进入 `graph.query`。

## 二开扩展点
- 替换 LLM 提供方（实现 `infra.AIClient` 接口）
- 自定义 prompt 模板与 few-shot 示例库
- 加严校验规则（如限制查询深度、强制 LIMIT）
- 缓存热门 question→cypher 映射，降低 LLM 调用成本
