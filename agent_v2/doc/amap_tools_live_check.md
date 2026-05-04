# 高德地图 Tools 真实取数验证报告

## 结论

本次已发起真实请求：PASS 1，FAIL 13，SKIP 0。失败项见明细。

## 本次运行

- 运行时间：2026-05-04 17:03:35 CST
- 配置文件中的高德 key 来源：`amap.api_key`
- baseurl：`https://restapi.amap.com/v4`
- timeout_seconds：`10`
- retry.max_retries：`2`
- 复跑命令：`AMAP_MAPS_API_KEY=你的高德Key go run ./cmd/amap_live_check -out doc/amap_tools_live_check.md`

## 明细

| Tool | Endpoint | 状态 | 延迟(ms) | 证据 / 错误 |
|---|---:|---:|---:|---|
| `amap_poi_keyword_search` | `` | FAIL | 0 | amap api returned failure endpoint=/place/text status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_poi_around_search` | `` | FAIL | 0 | amap api returned failure endpoint=/place/around status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_poi_detail` | `` | FAIL | 0 | amap api returned failure endpoint=/place/detail status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_input_tips` | `` | FAIL | 0 | amap api returned failure endpoint=/assistant/inputtips status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_geocode` | `` | FAIL | 0 | amap api returned failure endpoint=/geocode/geo status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_regeocode` | `` | FAIL | 0 | amap api returned failure endpoint=/geocode/regeo status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_distance` | `` | FAIL | 0 | amap api returned failure endpoint=/distance status=20000 info=INVALID_PARAMS infocode=请求参数（origins and destinations must not empty）非法，请参照接口开发文档进行调整 |
| `amap_route_walking` | `` | FAIL | 0 | amap api returned failure endpoint=/direction/walking status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_route_transit` | `` | FAIL | 0 | amap api returned failure endpoint=/direction/transit/integrated status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_route_driving` | `` | FAIL | 0 | amap api returned failure endpoint=/direction/driving status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_route_bicycling` | `/direction/bicycling` | PASS | 142 | paths_count=1; distance_m=390; duration_s=94 |
| `amap_district_search` | `` | FAIL | 0 | amap api returned failure endpoint=/config/district status=10002 info=SERVICE_NOT_AVAILABLE infocode=service config doesn't exist. |
| `amap_ip_location` | `` | FAIL | 0 | amap api returned failure endpoint=/ip status=30001 info=ENGINE_RESPONSE_DATA_ERROR infocode=null:null:null |
| `amap_static_map` | `` | FAIL | 0 | amap static map returned non-image content type "application/json;charset=UTF-8" |

## 覆盖范围

- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 tool wrapper 发起调用，不绕过工具层。
- 静态地图开启 `validate=true` 时会实际请求图片并验证 HTTP 状态与 Content-Type；报告中只保存脱敏信息，不保存 API Key。
- POI ID 查询优先使用本轮关键字搜索返回的第一个 POI ID，避免依赖固定样例 ID。
- 常规 `go test ./...` 使用本地 `httptest` 覆盖 endpoint、query 参数、key/output 注入，不消耗高德额度。
