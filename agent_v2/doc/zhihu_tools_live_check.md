# 知乎 Guide Material Tool 真实取数验证报告

## 结论

本次已通过 `zhihu_guide_material` 发起真实知乎素材采集，并成功返回筛选后的攻略素材结构化结果。

## 本次运行

- 运行时间：2026-05-05 19:21:46 CST
- topic：`大阪旅游攻略`
- openapi_base_url：`https://developer.zhihu.com`
- zhihu_search_url：``
- access_secret_configured：`true`
- 复跑命令：`go run ./cmd/zhihu_live_check -topic "大阪旅游攻略" -query-count 3 -per-query-count 3 -selected-count 3`

## 明细

| Tool | 状态 | 延迟(ms) | 证据 / 错误 |
|---|---:|---:|---|
| `zhihu_guide_material` | PASS | 7147 | run_id=20260505_192139_大阪旅游攻略; query_count=3; raw_count=9; deduped_count=6; review_pool_count=4; selected_count=3; first_title=日本东京+大阪旅行全攻略:省心省力更省钱 - 知乎; first_url=https://zhuanlan.zhihu.com/p/713474588?utm_medium=openapi_platform&utm_source=4818dc34; first_intent=overview; first_score=53.0 |

## 覆盖范围

- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 `zhihu_guide_material` wrapper 发起调用，不绕过工具层。
- 工具内部多轮调用知乎搜索脚本，完成取数、去重、过滤、评分和 `selected_for_llm` 生成。
- 工具不做业务文件落盘；验证报告只确认结构化返回包含 `selected_for_llm` 和审核决策数据。
- 报告只记录是否配置密钥，不输出 Access Secret。
