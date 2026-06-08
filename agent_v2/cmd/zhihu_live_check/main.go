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

	"agent_v2/config"
	zhihutools "agent_v2/tools"

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
	outPath := flag.String("out", "doc/zhihu_tools_live_check.md", "Markdown 报告输出路径")
	topic := flag.String("topic", "大阪旅游攻略", "攻略素材主题")
	queryCount := flag.Int("query-count", 3, "验证用搜索词数量")
	perQueryCount := flag.Int("per-query-count", 3, "每个搜索词返回数量，范围 1-10")
	selectedCount := flag.Int("selected-count", 3, "验证用最终素材数量")
	timeout := flag.Duration("timeout", 90*time.Second, "整体验证超时时间")
	flag.Parse()

	if err := run(*configPath, *outPath, *topic, *queryCount, *perQueryCount, *selectedCount, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "zhihu live check failed: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath, outPath, topic string, queryCount, perQueryCount, selectedCount int, timeout time.Duration) error {
	_ = config.Load(configPath)
	cfg := config.Cfg.Zhihu

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if strings.TrimSpace(cfg.AccessSecret) == "" && strings.TrimSpace(os.Getenv("ZHIHU_ACCESS_SECRET")) == "" {
		return writeReport(outPath, cfg, topic, false, []checkResult{{
			Tool:   "zhihu_guide_material",
			Status: "SKIP",
			Error:  "missing zhihu.access_secret or ZHIHU_ACCESS_SECRET",
		}})
	}

	toolMap := mapTools(zhihutools.NewZhihuTools(cfg))
	result := callZhihuGuideMaterial(ctx, toolMap, zhihutools.ZhihuGuideMaterialInput{
		Topic:                topic,
		QueryCount:           queryCount,
		PerQueryCount:        perQueryCount,
		ReviewPoolSize:       maxInt(selectedCount*2, selectedCount),
		SelectedArticleCount: selectedCount,
	})
	return writeReport(outPath, cfg, topic, true, []checkResult{result})
}

func mapTools(items []agenttool.Tool) map[string]agenttool.Tool {
	out := make(map[string]agenttool.Tool, len(items))
	for _, item := range items {
		out[item.Declaration().Name] = item
	}
	return out
}

func callZhihuGuideMaterial(ctx context.Context, toolMap map[string]agenttool.Tool, input zhihutools.ZhihuGuideMaterialInput) checkResult {
	result := checkResult{Tool: "zhihu_guide_material"}
	start := time.Now()
	out, err := callTool(ctx, toolMap[result.Tool], input)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "FAIL"
		result.Error = err.Error()
		return result
	}
	resp, ok := out.(zhihutools.ZhihuGuideMaterialResult)
	if !ok {
		result.Status = "FAIL"
		result.Error = fmt.Sprintf("unexpected result type %T", out)
		return result
	}
	if resp.RawCount == 0 || resp.DedupedCount == 0 {
		result.Status = "FAIL"
		result.Error = zhihuGuideEvidence(resp)
		return result
	}
	if resp.SelectedCount == 0 || resp.SelectedForLLM.SelectedCount == 0 {
		result.Status = "FAIL"
		result.Error = zhihuGuideEvidence(resp)
		return result
	}
	if len(resp.Errors) > 0 {
		result.Status = "PASS_WITH_WARN"
		result.Evidence = zhihuGuideEvidence(resp)
		return result
	}
	result.Status = "PASS"
	result.Evidence = zhihuGuideEvidence(resp)
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

func zhihuGuideEvidence(resp zhihutools.ZhihuGuideMaterialResult) string {
	parts := []string{
		"run_id=" + resp.RunID,
		fmt.Sprintf("query_count=%d", resp.QueryCount),
		fmt.Sprintf("raw_count=%d", resp.RawCount),
		fmt.Sprintf("zhihu_search_raw_count=%d", resp.ZhihuSearchRawCount),
		fmt.Sprintf("global_search_raw_count=%d", resp.GlobalSearchRawCount),
		fmt.Sprintf("deduped_count=%d", resp.DedupedCount),
		fmt.Sprintf("review_pool_count=%d", resp.ReviewPoolCount),
		fmt.Sprintf("selected_count=%d", resp.SelectedCount),
	}
	if len(resp.SelectedForLLM.Items) > 0 {
		first := resp.SelectedForLLM.Items[0]
		parts = append(parts,
			"first_title="+first.Title,
			"first_url="+first.URL,
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

func writeReport(outPath string, cfg config.ZhihuConfig, topic string, attempted bool, results []checkResult) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	pass, fail, skip, warn := 0, 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case "PASS":
			pass++
		case "PASS_WITH_WARN":
			warn++
		case "SKIP":
			skip++
		default:
			fail++
		}
	}

	var b strings.Builder
	b.WriteString("# 知乎 Guide Material Tool 真实取数验证报告\n\n")
	b.WriteString("## 结论\n\n")
	if attempted && fail == 0 && skip == 0 {
		if warn > 0 {
			b.WriteString(fmt.Sprintf("本次已通过 `zhihu_guide_material` 发起真实知乎素材采集，主体链路成功，但存在 %d 个警告，见明细。\n\n", warn))
		} else {
			b.WriteString("本次已通过 `zhihu_guide_material` 发起真实知乎素材采集，并成功返回筛选后的攻略素材结构化结果。\n\n")
		}
	} else if attempted {
		b.WriteString(fmt.Sprintf("本次已发起真实请求：PASS %d，WARN %d，FAIL %d，SKIP %d。失败项见明细。\n\n", pass, warn, fail, skip))
	} else {
		b.WriteString("本次未能发起真实知乎请求：运行环境缺少知乎 Access Secret。代码级 tool wrapper 已有单元测试覆盖，真实取数需设置密钥后复跑。\n\n")
	}

	b.WriteString("## 本次运行\n\n")
	b.WriteString(fmt.Sprintf("- 运行时间：%s\n", now))
	b.WriteString(fmt.Sprintf("- topic：`%s`\n", topic))
	b.WriteString(fmt.Sprintf("- openapi_base_url：`%s`\n", cfg.OpenAPIBaseURL))
	b.WriteString(fmt.Sprintf("- zhihu_search_url：`%s`\n", cfg.ZhihuSearchURL))
	b.WriteString(fmt.Sprintf("- access_secret_configured：`%t`\n", strings.TrimSpace(cfg.AccessSecret) != "" || strings.TrimSpace(os.Getenv("ZHIHU_ACCESS_SECRET")) != ""))
	b.WriteString("- 复跑命令：`go run ./cmd/zhihu_live_check -topic \"大阪旅游攻略\" -query-count 3 -per-query-count 3 -selected-count 3`\n\n")

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
	b.WriteString("- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 `zhihu_guide_material` wrapper 发起调用，不绕过工具层。\n")
	b.WriteString("- 工具内部多轮调用知乎搜索脚本，完成取数、去重、过滤、评分和 `selected_for_llm` 生成。\n")
	b.WriteString("- 工具不做业务文件落盘；验证报告只确认结构化返回包含 `selected_for_llm` 和审核决策数据。\n")
	b.WriteString("- 报告只记录是否配置密钥，不输出 Access Secret。\n")

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
