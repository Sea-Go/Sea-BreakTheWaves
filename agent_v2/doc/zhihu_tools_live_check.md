# 知乎 Guide Material Tool 真实取数验证报告

## 结论

本次已通过 `zhihu_guide_material` 发起真实知乎素材采集，并成功返回筛选后的攻略素材结构化结果。

## 本次运行

- 运行时间：2026-05-08 15:45:59 CST
- topic：`大阪旅游攻略`
- openapi_base_url：`https://developer.zhihu.com`
- zhihu_search_url：``
- access_secret_configured：`true`
- 复跑命令：`go run ./cmd/zhihu_live_check -topic "大阪旅游攻略" -query-count 3 -per-query-count 3 -selected-count 3`

## 明细

| Tool | 状态 | 延迟(ms) | 证据 / 错误 |
|---|---:|---:|---|
| `zhihu_guide_material` | PASS | 5234 | run_id=20260508_154553_大阪旅游攻略; query_count=1; raw_count=4; zhihu_search_raw_count=2; global_search_raw_count=2; deduped_count=4; review_pool_count=2; selected_count=2; first_title=日本旅游只有3天东京和大阪怎么选? - 知乎; first_url=https://www.zhihu.com/question/7033566065/answer/2028588307982950527?utm_medium=openapi_platform&utm_source=4818dc34; first_intent=overview; first_content_brief=Title: 日本旅游只有3天东京和大阪怎么选? - 知乎 \| Content: 只有三天的话个人推荐大阪 当然这个大阪肯定不仅限于大阪市，而是近畿圈 然后我会放一些图片，都是我自己拍的，水平很糟糕，主要是证明这些我都去过，而不是看了几个攻略就在侃大山 day1 上午:落地关西机场 中午：坐haruka到梅田，适当逛一下梅田 下午：大阪城公园 晚上心斋桥 住宿推荐：不缺预算就住心斋桥or梅田附近，想要廉价酒店就去天王寺or新今宫，想要便宜一户建就去玉出 day2 上午:坐阪急/JR/京阪去京都（取决于你住的地方） ...; first_key_points=只有三天的话个人推荐大阪 / 当然这个大阪肯定不仅限于大阪市，而是近畿圈 / 然后我会放一些图片，都是我自己拍的，水平很糟糕，主要是证明这些我都去过，而不是看了几个攻略就在侃大山 / day1; first_score=51.8 |

## 覆盖范围

- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 `zhihu_guide_material` wrapper 发起调用，不绕过工具层。
- 工具内部多轮调用知乎搜索脚本，完成取数、去重、过滤、评分和 `selected_for_llm` 生成。
- 工具不做业务文件落盘；验证报告只确认结构化返回包含 `selected_for_llm` 和审核决策数据。
- 报告只记录是否配置密钥，不输出 Access Secret。
