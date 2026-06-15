package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent_v3/internal/config"
	amaptools "agent_v3/internal/tools/amap"

	agenttool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type checkResult struct {
	Tool      string
	Endpoint  string
	Status    string
	Evidence  string
	LatencyMs int64
	Error     string
}

type liveState struct {
	poiID           string
	geocodeLocation string
}

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	outPath := flag.String("out", "doc/amap_tools_live_check.md", "Markdown 报告输出路径")
	timeout := flag.Duration("timeout", 45*time.Second, "整体验证超时时间")
	flag.Parse()

	if err := run(*configPath, *outPath, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "amap live check failed: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath, outPath string, timeout time.Duration) error {
	_ = config.Load(configPath)
	cfg := config.Cfg.Amap.WithDefaults()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var results []checkResult
	if strings.TrimSpace(cfg.APIKey) == "" {
		results = skippedResults("缺少 amap.api_key，未发起真实高德请求")
		return writeReport(outPath, cfg, false, results)
	}

	toolMap := mapTools(amaptools.NewAmapTools(cfg))
	state := &liveState{}

	results = append(results, callAmapTool(ctx, toolMap, "amap_poi_keyword_search", amaptools.AmapPOIKeywordSearchInput{
		Keywords:  "外滩",
		City:      "上海",
		CityLimit: true,
		Offset:    3,
		Page:      1,
	}, func(resp amaptools.AmapResponse) string {
		poi := firstMap(sliceAt(resp.Raw, "pois"))
		state.poiID = strAt(poi, "id")
		return joinEvidence(
			"count="+strAt(resp.Raw, "count"),
			"first="+strAt(poi, "name"),
			"id="+state.poiID,
			"location="+strAt(poi, "location"),
		)
	}))

	results = append(results, callAmapTool(ctx, toolMap, "amap_poi_around_search", amaptools.AmapPOIAroundSearchInput{
		Location: "121.490317,31.239666",
		Keywords: "咖啡",
		Radius:   1000,
		Offset:   3,
		Page:     1,
	}, func(resp amaptools.AmapResponse) string {
		poi := firstMap(sliceAt(resp.Raw, "pois"))
		return joinEvidence("count="+strAt(resp.Raw, "count"), "first="+strAt(poi, "name"), "distance="+strAt(poi, "distance"))
	}))

	detailID := state.poiID
	if detailID == "" {
		detailID = "B000A8UIN8"
	}
	results = append(results, callAmapTool(ctx, toolMap, "amap_poi_detail", amaptools.AmapPOIDetailInput{
		ID:         detailID,
		Extensions: "base",
	}, func(resp amaptools.AmapResponse) string {
		poi := firstMap(sliceAt(resp.Raw, "pois"))
		return joinEvidence("queried_id="+detailID, "name="+strAt(poi, "name"), "type="+strAt(poi, "type"))
	}))

	results = append(results, callAmapTool(ctx, toolMap, "amap_input_tips", amaptools.AmapInputTipsInput{
		Keywords:  "迪士尼",
		City:      "上海",
		CityLimit: true,
		Datatype:  "all",
	}, func(resp amaptools.AmapResponse) string {
		tip := firstMap(sliceAt(resp.Raw, "tips"))
		return joinEvidence("count="+fmt.Sprint(len(sliceAt(resp.Raw, "tips"))), "first="+strAt(tip, "name"), "district="+strAt(tip, "district"))
	}))

	results = append(results, callAmapTool(ctx, toolMap, "amap_geocode", amaptools.AmapGeocodeInput{
		Address: "上海外滩",
		City:    "上海",
	}, func(resp amaptools.AmapResponse) string {
		geo := firstMap(sliceAt(resp.Raw, "geocodes"))
		state.geocodeLocation = strAt(geo, "location")
		return joinEvidence("count="+strAt(resp.Raw, "count"), "address="+strAt(geo, "formatted_address"), "location="+state.geocodeLocation)
	}))

	regeoLocation := firstNonEmpty(state.geocodeLocation, "121.490317,31.239666")
	results = append(results, callAmapTool(ctx, toolMap, "amap_regeocode", amaptools.AmapRegeoInput{
		Location:   regeoLocation,
		Radius:     1000,
		Extensions: "all",
	}, func(resp amaptools.AmapResponse) string {
		regeo := mapAt(resp.Raw, "regeocode")
		return joinEvidence("location="+regeoLocation, "address="+strAt(regeo, "formatted_address"))
	}))

	results = append(results, callAmapTool(ctx, toolMap, "amap_distance", amaptools.AmapDistanceInput{
		Origins:     []string{"121.490317,31.239666", "121.475362,31.223667"},
		Destination: "121.667630,31.141156",
		Type:        0,
	}, func(resp amaptools.AmapResponse) string {
		item := firstMap(sliceAt(resp.Raw, "results"))
		return joinEvidence("count="+strAt(resp.Raw, "count"), "first_distance_m="+strAt(item, "distance"))
	}))

	results = append(results, callAmapTool(ctx, toolMap, "amap_route_walking", amaptools.AmapWalkingRouteInput{
		Origin:      "121.490317,31.239666",
		Destination: "121.492000,31.241000",
	}, routeEvidence("paths")))

	results = append(results, callAmapTool(ctx, toolMap, "amap_route_transit", amaptools.AmapTransitRouteInput{
		Origin:      "121.490317,31.239666",
		Destination: "121.667630,31.141156",
		City:        "上海",
		Extensions:  "base",
	}, routeEvidence("transits")))

	results = append(results, callAmapTool(ctx, toolMap, "amap_route_driving", amaptools.AmapDrivingRouteInput{
		Origin:      "121.490317,31.239666",
		Destination: "121.667630,31.141156",
		Extensions:  "base",
	}, routeEvidence("paths")))

	results = append(results, callAmapTool(ctx, toolMap, "amap_route_bicycling", amaptools.AmapBicyclingRouteInput{
		Origin:      "121.490317,31.239666",
		Destination: "121.492000,31.241000",
	}, routeEvidence("paths")))

	results = append(results, callAmapTool(ctx, toolMap, "amap_district_search", amaptools.AmapDistrictSearchInput{
		Keywords:    "上海",
		Subdistrict: 1,
		Extensions:  "base",
	}, func(resp amaptools.AmapResponse) string {
		district := firstMap(sliceAt(resp.Raw, "districts"))
		return joinEvidence("count="+strAt(resp.Raw, "count"), "name="+strAt(district, "name"), "adcode="+strAt(district, "adcode"))
	}))

	results = append(results, callAmapTool(ctx, toolMap, "amap_ip_location", amaptools.AmapIPLocationInput{
		IP: "114.247.50.2",
	}, func(resp amaptools.AmapResponse) string {
		return joinEvidence("province="+strAt(resp.Raw, "province"), "city="+strAt(resp.Raw, "city"), "rectangle="+strAt(resp.Raw, "rectangle"))
	}))

	results = append(results, callStaticMapTool(ctx, toolMap, amaptools.AmapStaticMapInput{
		Location: "121.490317,31.239666",
		Zoom:     13,
		Size:     "600*400",
		Traffic:  true,
		Validate: true,
	}))

	return writeReport(outPath, cfg, true, results)
}

func mapTools(items []agenttool.Tool) map[string]agenttool.Tool {
	out := make(map[string]agenttool.Tool, len(items))
	for _, item := range items {
		out[item.Declaration().Name] = item
	}
	return out
}

func callAmapTool(
	ctx context.Context,
	toolMap map[string]agenttool.Tool,
	name string,
	args any,
	evidence func(amaptools.AmapResponse) string,
) checkResult {
	result := checkResult{Tool: name}
	out, err := callTool(ctx, toolMap[name], args)
	if err != nil {
		result.Status = "FAIL"
		result.Error = err.Error()
		return result
	}
	resp, ok := out.(amaptools.AmapResponse)
	if !ok {
		result.Status = "FAIL"
		result.Error = fmt.Sprintf("unexpected result type %T", out)
		return result
	}
	result.Endpoint = resp.Endpoint
	result.LatencyMs = resp.LatencyMs
	if !resp.OK {
		result.Status = "FAIL"
		result.Error = joinEvidence(resp.Info, resp.Infocode)
		return result
	}
	result.Status = "PASS"
	result.Evidence = evidence(resp)
	return result
}

func callStaticMapTool(ctx context.Context, toolMap map[string]agenttool.Tool, args amaptools.AmapStaticMapInput) checkResult {
	result := checkResult{Tool: "amap_static_map"}
	out, err := callTool(ctx, toolMap[result.Tool], args)
	if err != nil {
		result.Status = "FAIL"
		result.Error = err.Error()
		return result
	}
	resp, ok := out.(amaptools.AmapStaticMapResult)
	if !ok {
		result.Status = "FAIL"
		result.Error = fmt.Sprintf("unexpected result type %T", out)
		return result
	}
	result.Endpoint = resp.Endpoint
	result.LatencyMs = resp.LatencyMs
	if !resp.OK {
		result.Status = "FAIL"
		result.Error = fmt.Sprintf("status_code=%d content_type=%s", resp.StatusCode, resp.ContentType)
		return result
	}
	result.Status = "PASS"
	result.Evidence = joinEvidence(
		"validated="+fmt.Sprint(resp.Validated),
		"status_code="+fmt.Sprint(resp.StatusCode),
		"content_type="+resp.ContentType,
		"content_length="+fmt.Sprint(resp.ContentLength),
		"url_key_redacted="+fmt.Sprint(!strings.Contains(resp.URLRedacted, cfgLeakNeedle())),
	)
	return result
}

func callTool(ctx context.Context, item agenttool.Tool, args any) (any, error) {
	if item == nil {
		return nil, fmt.Errorf("tool not found")
	}
	callable, ok := item.(agenttool.CallableTool)
	if !ok {
		return nil, fmt.Errorf("tool %s is not callable", item.Declaration().Name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return callable.Call(ctx, raw)
}

func routeEvidence(key string) func(amaptools.AmapResponse) string {
	return func(resp amaptools.AmapResponse) string {
		route := mapAt(resp.Raw, "route")
		items := itemsAt(route, key)
		if len(items) == 0 {
			data := mapAt(resp.Raw, "data")
			items = itemsAt(data, key)
		}
		first := firstMap(items)
		return joinEvidence(
			key+"_count="+fmt.Sprint(len(items)),
			"distance_m="+strAt(first, "distance"),
			"duration_s="+strAt(first, "duration"),
		)
	}
}

func skippedResults(reason string) []checkResult {
	names := []string{
		"amap_poi_keyword_search",
		"amap_poi_around_search",
		"amap_poi_detail",
		"amap_input_tips",
		"amap_geocode",
		"amap_regeocode",
		"amap_distance",
		"amap_route_walking",
		"amap_route_transit",
		"amap_route_driving",
		"amap_route_bicycling",
		"amap_district_search",
		"amap_ip_location",
		"amap_static_map",
	}
	results := make([]checkResult, 0, len(names))
	for _, name := range names {
		results = append(results, checkResult{Tool: name, Status: "SKIP", Error: reason})
	}
	return results
}

func writeReport(outPath string, cfg config.AmapConfig, attempted bool, results []checkResult) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	pass, fail, skip := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case "PASS":
			pass++
		case "SKIP":
			skip++
		default:
			fail++
		}
	}

	var b strings.Builder
	b.WriteString("# 高德地图 Tools 真实取数验证报告\n\n")
	b.WriteString("## 结论\n\n")
	if attempted && fail == 0 && pass == len(results) {
		b.WriteString("本次已对 14 个高德地图 tools 发起真实请求，全部成功返回数据。\n\n")
	} else if attempted {
		b.WriteString(fmt.Sprintf("本次已发起真实请求：PASS %d，FAIL %d，SKIP %d。失败项见明细。\n\n", pass, fail, skip))
	} else {
		b.WriteString("本次未能发起真实高德请求：运行环境缺少高德 API Key。代码级 tool wrapper 已有单元测试覆盖，真实取数需设置 key 后复跑。\n\n")
	}

	b.WriteString("## 本次运行\n\n")
	b.WriteString(fmt.Sprintf("- 运行时间：%s\n", now))
	b.WriteString("- 配置文件中的高德 key 来源：`amap.api_key`\n")
	b.WriteString(fmt.Sprintf("- baseurl：`%s`\n", cfg.BaseURL))
	b.WriteString(fmt.Sprintf("- timeout_seconds：`%d`\n", cfg.TimeoutSeconds))
	b.WriteString(fmt.Sprintf("- retry.max_retries：`%d`\n", cfg.Retry.MaxRetries))
	b.WriteString("- 复跑命令：`go run ./cmd/amap_live_check -out doc/amap_tools_live_check.md`\n\n")

	b.WriteString("## 明细\n\n")
	b.WriteString("| Tool | Endpoint | 状态 | 延迟(ms) | 证据 / 错误 |\n")
	b.WriteString("|---|---:|---:|---:|---|\n")
	for _, result := range results {
		detail := result.Evidence
		if detail == "" {
			detail = result.Error
		}
		b.WriteString(fmt.Sprintf(
			"| `%s` | `%s` | %s | %d | %s |\n",
			escapeCell(result.Tool),
			escapeCell(result.Endpoint),
			escapeCell(result.Status),
			result.LatencyMs,
			escapeCell(detail),
		))
	}

	b.WriteString("\n## 覆盖范围\n\n")
	b.WriteString("- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 tool wrapper 发起调用，不绕过工具层。\n")
	b.WriteString("- 静态地图开启 `validate=true` 时会实际请求图片并验证 HTTP 状态与 Content-Type；报告中只保存脱敏信息，不保存 API Key。\n")
	b.WriteString("- POI ID 查询优先使用本轮关键字搜索返回的第一个 POI ID，避免依赖固定样例 ID。\n")
	b.WriteString("- 常规 `go test ./...` 使用本地 `httptest` 覆盖 endpoint、query 参数、key/output 注入，不消耗高德额度。\n")

	return os.WriteFile(outPath, []byte(b.String()), 0o644)
}

func mapAt(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return nil
	}
	item, _ := raw[key].(map[string]any)
	return item
}

func sliceAt(raw map[string]any, key string) []any {
	if raw == nil {
		return nil
	}
	items, _ := raw[key].([]any)
	return items
}

func itemsAt(raw map[string]any, key string) []any {
	if raw == nil {
		return nil
	}
	switch value := raw[key].(type) {
	case []any:
		return value
	case map[string]any:
		return []any{value}
	default:
		return nil
	}
}

func firstMap(items []any) map[string]any {
	if len(items) == 0 {
		return nil
	}
	item, _ := items[0].(map[string]any)
	return item
}

func strAt(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	switch value := raw[key].(type) {
	case string:
		return value
	case float64:
		return fmt.Sprintf("%.0f", value)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func joinEvidence(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "; ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func cfgLeakNeedle() string {
	key := strings.TrimSpace(config.Cfg.Amap.APIKey)
	if key == "" {
		return "\x00"
	}
	return key
}
