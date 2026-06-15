# 高德地图 Tools 文件说明

## 组织原则

`agent_v3/internal/tools/amap` 下的高德地图工具按“一个功能一个文件”组织。每个具体功能文件应同时包含：

- 该功能的 input struct。
- 该功能的 `function.NewFunctionTool(...)` 包装函数。
- 该功能的参数校验、query 拼装和调用实现。

`amap_tools.go` 只做聚合注册，不写具体业务接口实现。`amap_common.go` 只放所有高德工具共享的 HTTP/runtime、响应结构和参数 helper，不代表某一个业务功能。

## 文件清单

| 文件 | 作用 |
|---|---|
| `amap_tools.go` | 高德工具集合入口。提供 `AmapToolSet`、`NewDefaultAmapToolSet()`、`NewAmapToolSet()`、`NewDefaultAmapTools()`、`NewAmapTools()`，并把所有单功能 tool 聚合成 `[]tool.Tool`。 |
| `amap_common.go` | 高德工具共享运行时与公共 helper。负责读取 `config.AmapConfig`、HTTP GET、重试、公共 query 注入、响应标准化、静态地图底层请求、URL 脱敏和 query 参数 helper。 |
| `amap_poi_keyword_search.go` | 关键字搜索工具。用于按关键词或 POI 类型搜索景点、餐厅、酒店、商圈、博物馆等候选 POI。 |
| `amap_poi_around_search.go` | 周边搜索工具。用于根据中心点坐标查询附近餐厅、咖啡、亲子设施、地铁站等 POI。 |
| `amap_poi_detail.go` | POI ID 查询工具。用于根据搜索或输入提示返回的 POI ID 查询地点详情。 |
| `amap_input_tips.go` | 输入提示工具。用于用户输入地点关键词时做自动补全、纠错，并获取候选 POI ID。 |
| `amap_geocode.go` | 地理编码工具。用于把结构化地址或地名转换为经纬度。 |
| `amap_regeocode.go` | 逆地理编码工具。用于把经纬度转换为地址、行政区、附近 POI/AOI 等位置信息。 |
| `amap_distance.go` | 距离测量工具。用于快速估算多个起点到目标点的直线、驾车或步行距离，辅助行程排序。 |
| `amap_walking_route.go` | 步行路径规划工具。用于景区内、商圈内、地铁站到景点等短距离步行方案。 |
| `amap_transit_route.go` | 公交路径规划工具。用于城市自由行中的公交、地铁等公共交通换乘方案。 |
| `amap_driving_route.go` | 驾车路径规划工具。用于自驾游、包车游、郊区景点串联和途经点路线。 |
| `amap_bicycling_route.go` | 骑行路径规划工具。用于城市骑行、景区骑行和慢游路线。 |
| `amap_district_search.go` | 行政区域查询工具。用于获取城市、区县 adcode，支持限定搜索范围、天气查询前置标准化等场景。 |
| `amap_ip_location.go` | IP 定位工具。用于用户未填写城市时粗略判断默认城市，不适合精确定位。 |
| `amap_static_map.go` | 静态地图工具。用于生成行程路线或景点分布图的脱敏 URL，并可选择实际请求验证图片状态。 |
| `amap_tools_test.go` | 高德工具测试。使用 `httptest` 验证各 tool 的 endpoint、query 参数、公共 key/output 注入、声明完整性和配置错误处理。 |

## 新增工具约定

新增高德能力时，优先新增一个独立文件，例如 `amap_weather.go`。该文件内放完整的 input struct、`newAmapWeatherTool(...)` 和具体实现。然后只在 `amap_tools.go` 的 `newAmapTools(...)` 中追加注册，不要把实现写回聚合文件，也不要重新建立按 `input`、`route`、`client` 分类的文件。
