package search

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// ============================================================================
// 该文件测试 IntentParser，覆盖：
//   - 规则路径（时效 / 意图 / 实体识别）
//   - LLM 路径（结构化输出解析）
//   - LLM 失败回退规则 / 关闭兜底返回错误 / 非法 JSON 回退
//   - 空查询 / nil LLM / 编译期断言
// ============================================================================

// 编译期断言：*IntentParser 实现 QueryUnderstander interface。
var _ QueryUnderstander = (*IntentParser)(nil)

// stubLLMClient 模拟 LLMClient（understand / rewrite 测试共用）。
type stubLLMClient struct {
	fn    func(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error)
	mu    sync.Mutex
	calls int
}

func (s *stubLLMClient) CompleteWithStructuredOutput(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(ctx, prompt, schema)
	}
	return nil, nil
}

// TestIntentParser_RuleUnderstand 验证规则路径的意图与时效识别。
func TestIntentParser_RuleUnderstand(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantLabel string
		wantTime  string
	}{
		{"最新", "最新的 ai 论文", "informational", "latest"},
		{"今天", "今天新闻", "informational", "latest"},
		{"比较", "iphone vs android", "comparative", "ever"},
		{"对比", "对比 a 和 b 的区别", "comparative", "ever"},
		{"购买", "怎么买 iphone", "transactional", "ever"},
		{"价格", "iphone 价格", "transactional", "ever"},
		{"一周内", "最近一周新闻", "informational", "within_7d"},
		{"7天", "7天内更新", "informational", "within_7d"},
		{"30天", "最近30天文章", "informational", "within_30d"},
		{"本月", "本月热门", "informational", "within_30d"},
		{"默认", "推荐系统架构", "informational", "ever"},
	}
	p := NewIntentParser(nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			intent, err := p.Understand(context.Background(), c.query)
			if err != nil {
				t.Fatalf("Understand 返回错误: %v", err)
			}
			if intent.Label != c.wantLabel {
				t.Errorf("Label = %q, 期望 %q (query=%q)", intent.Label, c.wantLabel, c.query)
			}
			if intent.TimeIntent != c.wantTime {
				t.Errorf("TimeIntent = %q, 期望 %q (query=%q)", intent.TimeIntent, c.wantTime, c.query)
			}
		})
	}
}

// TestIntentParser_RuleUnderstand_Entities 验证规则路径实体识别。
func TestIntentParser_RuleUnderstand_Entities(t *testing.T) {
	p := NewIntentParser(nil)
	intent, err := p.Understand(context.Background(), "作者:张三 的文章 #科技#")
	if err != nil {
		t.Fatalf("Understand 返回错误: %v", err)
	}
	found := map[string]string{}
	for _, e := range intent.Entities {
		found[e.Type] = e.Name
	}
	if found["Author"] != "张三" {
		t.Errorf("Author 实体 = %q, 期望 张三", found["Author"])
	}
	if found["Tag"] != "科技" {
		t.Errorf("Tag 实体 = %q, 期望 科技", found["Tag"])
	}
}

// TestIntentParser_EmptyQuery 验证空查询返回默认意图。
func TestIntentParser_EmptyQuery(t *testing.T) {
	p := NewIntentParser(nil)
	intent, err := p.Understand(context.Background(), "")
	if err != nil {
		t.Fatalf("Understand 返回错误: %v", err)
	}
	if intent.Label != "informational" {
		t.Errorf("Label = %q, 期望 informational", intent.Label)
	}
	if intent.TimeIntent != "ever" {
		t.Errorf("TimeIntent = %q, 期望 ever", intent.TimeIntent)
	}
}

// TestIntentParser_NilLLM 验证 nil LLM 走规则路径。
func TestIntentParser_NilLLM(t *testing.T) {
	p := NewIntentParser(nil)
	intent, err := p.Understand(context.Background(), "最新新闻")
	if err != nil {
		t.Fatalf("Understand 返回错误: %v", err)
	}
	if intent.TimeIntent != "latest" {
		t.Errorf("TimeIntent = %q, 期望 latest", intent.TimeIntent)
	}
	if p.llm != nil {
		t.Errorf("llm 应为 nil")
	}
}

// TestIntentParser_LLMPath 验证 LLM 结构化输出路径。
func TestIntentParser_LLMPath(t *testing.T) {
	llm := &stubLLMClient{
		fn: func(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error) {
			return json.RawMessage(`{"label":"comparative","time_intent":"within_7d","entities":[{"name":"iphone","type":"IP"},{"name":"android","type":"IP"}]}`), nil
		},
	}
	p := NewIntentParser(llm)
	intent, err := p.Understand(context.Background(), "iphone vs android")
	if err != nil {
		t.Fatalf("Understand 返回错误: %v", err)
	}
	if intent.Label != "comparative" {
		t.Errorf("Label = %q, 期望 comparative", intent.Label)
	}
	if intent.TimeIntent != "within_7d" {
		t.Errorf("TimeIntent = %q, 期望 within_7d", intent.TimeIntent)
	}
	if len(intent.Entities) != 2 {
		t.Fatalf("实体数 = %d, 期望 2", len(intent.Entities))
	}
	if intent.Entities[0].Name != "iphone" || intent.Entities[0].Type != "IP" {
		t.Errorf("首个实体 = %+v, 期望 iphone/IP", intent.Entities[0])
	}
	if llm.calls != 1 {
		t.Errorf("LLM 调用次数 = %d, 期望 1", llm.calls)
	}
}

// TestIntentParser_LLMFailFallback 验证 LLM 失败回退规则。
func TestIntentParser_LLMFailFallback(t *testing.T) {
	llm := &stubLLMClient{
		fn: func(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error) {
			return nil, errors.New("llm unavailable")
		},
	}
	p := NewIntentParser(llm) // 默认启用规则兜底
	intent, err := p.Understand(context.Background(), "最新新闻")
	if err != nil {
		t.Fatalf("LLM 失败应回退规则而非返回错误: %v", err)
	}
	if intent.TimeIntent != "latest" {
		t.Errorf("回退规则后 TimeIntent = %q, 期望 latest", intent.TimeIntent)
	}
}

// TestIntentParser_LLMFailNoFallback 验证关闭兜底时 LLM 失败返回错误。
func TestIntentParser_LLMFailNoFallback(t *testing.T) {
	llm := &stubLLMClient{
		fn: func(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error) {
			return nil, errors.New("llm unavailable")
		},
	}
	p := NewIntentParser(llm, WithRuleFallbackEnabled(false))
	_, err := p.Understand(context.Background(), "最新新闻")
	if err == nil {
		t.Fatalf("关闭兜底时 LLM 失败应返回错误")
	}
}

// TestIntentParser_LLMInvalidJSON_Fallback 验证 LLM 返回非法 JSON 时回退规则。
func TestIntentParser_LLMInvalidJSON_Fallback(t *testing.T) {
	llm := &stubLLMClient{
		fn: func(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error) {
			return json.RawMessage(`{not json`), nil
		},
	}
	p := NewIntentParser(llm)
	intent, err := p.Understand(context.Background(), "怎么买 iphone")
	if err != nil {
		t.Fatalf("非法 JSON 应回退规则而非返回错误: %v", err)
	}
	if intent.Label != "transactional" {
		t.Errorf("回退规则后 Label = %q, 期望 transactional", intent.Label)
	}
}

// TestNormalizeLabel 验证意图标签归一化。
func TestNormalizeLabel(t *testing.T) {
	if got := normalizeLabel("Comparative"); got != "comparative" {
		t.Errorf("normalizeLabel(Comparative) = %q", got)
	}
	if got := normalizeLabel("unknown"); got != "informational" {
		t.Errorf("非法标签应降级 informational, got %q", got)
	}
}

// TestNormalizeTimeIntent 验证时效意图归一化。
func TestNormalizeTimeIntent(t *testing.T) {
	if got := normalizeTimeIntent("LATEST"); got != "latest" {
		t.Errorf("normalizeTimeIntent(LATEST) = %q", got)
	}
	if got := normalizeTimeIntent("foo"); got != "ever" {
		t.Errorf("非法时效应降级 ever, got %q", got)
	}
}
