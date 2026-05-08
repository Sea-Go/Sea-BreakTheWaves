# B 站 Guide Material Tool 真实取数验证报告

## 结论

本次已通过 `bilibili_guide_material` 发起真实 B 站素材采集，并成功返回筛选后的视频攻略素材结构化结果。

## 本次运行

- 运行时间：2026-05-08 15:46:02 CST
- topic：`成都旅游攻略 美食 景点`
- cookie_configured：`false`
- 复跑命令：`go run ./cmd/bilibili_live_check -topic "成都旅游攻略 美食 景点" -query-count 3 -per-query-count 5 -selected-count 3`

## 明细

| Tool | 状态 | 延迟(ms) | 证据 / 错误 |
|---|---:|---:|---|
| `bilibili_guide_material` | PASS | 8777 | run_id=20260508_154553_成都旅游攻略_美食_景点; query_count=2; raw_count=8; deduped_count=7; review_pool_count=4; selected_count=2; first_title=此生一定要去的国内八大美食之都; first_url=http://www.bilibili.com/video/av113594469452461; first_bvid=BV1F6iUYqEtr; first_intent=overview; first_content_brief=Title: 此生一定要去的国内八大美食之都 \| Content: 全都吃过就不枉此生 \| Duration: 1:24; first_key_points=全都吃过就不枉此生; first_score=66.0 |

## 覆盖范围

- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 `bilibili_guide_material` wrapper 发起调用，不绕过工具层。
- 工具内部多轮调用 B 站搜索脚本，完成取数、去重、过滤、评分和 `selected_for_llm` 生成。
- 公开搜索默认不要求 Cookie；如果配置了 `bilibili.cookie` 或 `BILIBILI_COOKIE`，脚本会带上登录态。
- 报告只记录是否配置 Cookie，不输出 Cookie 内容。
