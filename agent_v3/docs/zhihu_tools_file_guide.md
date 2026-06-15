# 知乎 Tools 文件说明

## 组织原则

`agent_v3/internal/tools/zhihu` 下的知乎工具按“底层能力和组合能力分离”组织。

- 底层取数文件只负责调用外部接口或 skill 脚本、参数校验和结果标准化。
- 组合工具文件负责把底层取数能力编排成 agent 可直接使用的完整能力。
- 公开给 agent 的 tool 必须能独立完成一个清晰任务，并返回稳定的结构化结果。

`zhihu_tools.go` 只做聚合注册，不写具体业务接口实现。`zhihu_search.go` 是知乎搜索底层运行时，供组合工具复用，不直接作为 agent tool 暴露。知乎 tool 本体不做业务文件落盘；真实取数验证报告由 `cmd/zhihu_live_check` 写入 `docs/zhihu_tools_live_check.md`。

## 文件清单

| 文件 | 作用 |
|---|---|
| `zhihu_tools.go` | 知乎工具集合入口。提供 `ZhihuToolSet`、`NewDefaultZhihuToolSet()`、`NewZhihuToolSet()`、`NewDefaultZhihuTools()`、`NewZhihuTools()`，并把公开 agent tools 聚合成 `[]tool.Tool`。当前只公开 `zhihu_guide_material`。 |
| `zhihu_search.go` | 知乎搜索底层运行时。负责调用 `skills/zhihu-search/scripts/zhihu-search.py`，注入知乎开放平台配置，并把脚本输出解码为规范化搜索结果。 |
| `zhihu_guide_material.go` | 攻略素材采集组合工具。根据 topic 生成多组 query，多轮调用知乎搜索后完成去重、过滤、评分、审核候选池选择和 `selected_for_llm` 生成，并返回审核决策数据。 |
| `zhihu_tools_test.go` | 知乎工具集合测试。验证 toolset 声明、公开工具名称、callable wrapper、底层 skill 参数传递和环境变量注入。 |
| `zhihu_guide_material_test.go` | 攻略素材组合工具测试。覆盖 query plan、重复 URL 去重、筛选评分、黑名单拒绝、人工审核决策应用、最终选择数量和 tool 调用。 |

## 新增工具约定

新增知乎能力时，先判断它是底层取数能力还是 agent 可直接使用的组合能力。

底层能力可以新增独立文件，例如 `zhihu_article_fetch.go`，文件内放完整的 input/result struct、runtime 方法、参数校验和外部接口调用实现。组合能力可以新增类似 `zhihu_guide_material.go` 的独立文件，内部复用底层能力，并提供 `newZhihuXxxTool(...)` 包装函数。

然后只在 `zhihu_tools.go` 的 `newZhihuTools(...)` 中追加需要公开给 agent 的组合工具，不要把实现写回聚合文件，也不要把只供内部复用的底层能力直接注册成公开 tool。

不要在 tool 本体中写业务产物文件。需要验证真实接口时，新增或扩展 `cmd/*_live_check`，并把报告写到 `doc` 目录下。
