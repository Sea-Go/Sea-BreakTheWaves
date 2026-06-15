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
	bilibilitools "agent_v3/internal/tools/bilibili"

	agenttool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type checkResult struct {
	Tool      string
	Status    string
	Evidence  string
	LatencyMs int64
	Error     string
}

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	outPath := flag.String("out", "doc/bilibili_tools_live_check.md", "Markdown 报告输出路径")
	topic := flag.String("topic", "成都旅游攻略 美食 景点", "B 站攻略素材主题")
	queryCount := flag.Int("query-count", 3, "验证用搜索词数量")
	perQueryCount := flag.Int("per-query-count", 5, "每个搜索词返回数量，范围 1-20")
	selectedCount := flag.Int("selected-count", 3, "验证用最终素材数量")
	timeout := flag.Duration("timeout", 120*time.Second, "整体验证超时时间")
	flag.Parse()

	if err := run(*configPath, *outPath, *topic, *queryCount, *perQueryCount, *selectedCount, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "bilibili live check failed: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath, outPath, topic string, queryCount, perQueryCount, selectedCount int, timeout time.Duration) error {
	_ = config.Load(configPath)
	cfg := config.Cfg.Bilibili
	if strings.TrimSpace(cfg.Cookie) == "" {
		cfg.Cookie = strings.TrimSpace(os.Getenv("BILIBILI_COOKIE"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	toolMap := mapTools(bilibilitools.NewBilibiliTools(cfg))
	result := callBilibiliGuideMaterial(ctx, toolMap, bilibilitools.BilibiliGuideMaterialInput{
		Topic:              topic,
		QueryCount:         queryCount,
		PerQueryCount:      perQueryCount,
		ReviewPoolSize:     maxInt(selectedCount*2, selectedCount),
		SelectedVideoCount: selectedCount,
		ShouldKeywords:     []string{"美食", "景点", "路线", "避坑", "vlog"},
	})
	return writeReport(outPath, cfg, topic, []checkResult{result})
}

func mapTools(items []agenttool.Tool) map[string]agenttool.Tool {
	out := make(map[string]agenttool.Tool, len(items))
	for _, item := range items {
		out[item.Declaration().Name] = item
	}
	return out
}

func callBilibiliGuideMaterial(ctx context.Context, toolMap map[string]agenttool.Tool, input bilibilitools.BilibiliGuideMaterialInput) checkResult {
	result := checkResult{Tool: "bilibili_guide_material"}
	start := time.Now()
	out, err := callTool(ctx, toolMap[result.Tool], input)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "FAIL"
		result.Error = err.Error()
		return result
	}
	resp, ok := out.(bilibilitools.BilibiliGuideMaterialResult)
	if !ok {
		result.Status = "FAIL"
		result.Error = fmt.Sprintf("unexpected result type %T", out)
		return result
	}
	if resp.RawCount == 0 || resp.DedupedCount == 0 {
		result.Status = "FAIL"
		result.Error = bilibiliGuideEvidence(resp)
		return result
	}
	if resp.SelectedCount == 0 || resp.SelectedForLLM.SelectedCount == 0 {
		result.Status = "FAIL"
		result.Error = bilibiliGuideEvidence(resp)
		return result
	}
	if len(resp.Errors) > 0 {
		result.Status = "PASS_WITH_WARN"
		result.Evidence = bilibiliGuideEvidence(resp)
		return result
	}
	result.Status = "PASS"
	result.Evidence = bilibiliGuideEvidence(resp)
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

func bilibiliGuideEvidence(resp bilibilitools.BilibiliGuideMaterialResult) string {
	parts := []string{
		"run_id=" + resp.RunID,
		fmt.Sprintf("query_count=%d", resp.QueryCount),
		fmt.Sprintf("raw_count=%d", resp.RawCount),
		fmt.Sprintf("deduped_count=%d", resp.DedupedCount),
		fmt.Sprintf("review_pool_count=%d", resp.ReviewPoolCount),
		fmt.Sprintf("selected_count=%d", resp.SelectedCount),
	}
	if len(resp.SelectedForLLM.Items) > 0 {
		first := resp.SelectedForLLM.Items[0]
		parts = append(parts,
			"first_title="+first.Title,
			"first_url="+first.URL,
			"first_bvid="+first.BVID,
			"first_intent="+first.Intent,
			"first_content_brief="+truncateEvidence(first.ContentBrief, 260),
			"first_key_points="+truncateEvidence(strings.Join(first.KeyPoints, " / "), 260),
			fmt.Sprintf("first_score=%.1f", first.Score),
		)
	}
	if len(resp.Errors) > 0 {
		parts = append(parts, fmt.Sprintf("query_errors=%d", len(resp.Errors)))
		parts = append(parts, "first_query_error="+resp.Errors[0].Query+": "+resp.Errors[0].Error)
	}
	return joinEvidence(parts...)
}

func writeReport(outPath string, cfg config.BilibiliConfig, topic string, results []checkResult) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	pass, fail, warn := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case "PASS":
			pass++
		case "PASS_WITH_WARN":
			warn++
		default:
			fail++
		}
	}

	var b strings.Builder
	b.WriteString("# B 站 Guide Material Tool 真实取数验证报告\n\n")
	b.WriteString("## 结论\n\n")
	if fail == 0 {
		if warn > 0 {
			b.WriteString(fmt.Sprintf("本次已通过 `bilibili_guide_material` 发起真实 B 站素材采集，主体链路成功，但存在 %d 个警告，见明细。\n\n", warn))
		} else {
			b.WriteString("本次已通过 `bilibili_guide_material` 发起真实 B 站素材采集，并成功返回筛选后的视频攻略素材结构化结果。\n\n")
		}
	} else {
		b.WriteString(fmt.Sprintf("本次已发起真实请求：PASS %d，WARN %d，FAIL %d。失败项见明细。\n\n", pass, warn, fail))
	}

	b.WriteString("## 本次运行\n\n")
	b.WriteString(fmt.Sprintf("- 运行时间：%s\n", now))
	b.WriteString(fmt.Sprintf("- topic：`%s`\n", topic))
	b.WriteString(fmt.Sprintf("- cookie_configured：`%t`\n", strings.TrimSpace(cfg.Cookie) != "" || strings.TrimSpace(os.Getenv("BILIBILI_COOKIE")) != ""))
	b.WriteString("- 复跑命令：`go run ./cmd/bilibili_live_check -topic \"成都旅游攻略 美食 景点\" -query-count 3 -per-query-count 5 -selected-count 3`\n\n")

	b.WriteString("## 明细\n\n")
	b.WriteString("| Tool | 状态 | 延迟(ms) | 证据 / 错误 |\n")
	b.WriteString("|---|---:|---:|---|\n")
	for _, result := range results {
		detail := result.Evidence
		if detail == "" {
			detail = result.Error
		}
		b.WriteString(fmt.Sprintf(
			"| `%s` | %s | %d | %s |\n",
			escapeCell(result.Tool),
			escapeCell(result.Status),
			result.LatencyMs,
			escapeCell(detail),
		))
	}

	b.WriteString("\n## 覆盖范围\n\n")
	b.WriteString("- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 `bilibili_guide_material` wrapper 发起调用，不绕过工具层。\n")
	b.WriteString("- 工具内部多轮调用 B 站搜索脚本，完成取数、去重、过滤、评分和 `selected_for_llm` 生成。\n")
	b.WriteString("- 公开搜索默认不要求 Cookie；如果配置了 `bilibili.cookie` 或 `BILIBILI_COOKIE`，脚本会带上登录态。\n")
	b.WriteString("- 报告只记录是否配置 Cookie，不输出 Cookie 内容。\n")

	return os.WriteFile(outPath, []byte(b.String()), 0o644)
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

func truncateEvidence(value string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(value))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "..."
}

func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
