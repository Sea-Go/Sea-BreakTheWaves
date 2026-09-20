package search

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// ============================================================================
// 该文件测试 Rewriter，覆盖：
//   - 同义词词典 Lookup / 拼写纠正（编辑距离 1 + 纠错映射）
//   - nil LLM 规则路径 / LLM 路径合并 / LLM 失败回退
//   - levenshtein 单测 / 编译期断言
// ============================================================================

// 编译期断言：*Rewriter 实现 QueryRewriter interface。
var _ QueryRewriter = (*Rewriter)(nil)

// TestMapSynonymDict 验证内置同义词词典。
func TestMapSynonymDict(t *testing.T) {
	d := NewMapSynonymDict()
	d.Add("golang", []string{"go", "golang"}, []string{"rust"})

	// 命中
	syn, rel := d.Lookup("golang")
	if len(syn) != 2 || syn[0] != "go" {
		t.Errorf("synonyms = %v, 期望 [go golang]", syn)
	}
	if len(rel) != 1 || rel[0] != "rust" {
		t.Errorf("related = %v, 期望 [rust]", rel)
	}
	// 未命中
	syn2, rel2 := d.Lookup("unknown")
	if syn2 != nil || rel2 != nil {
		t.Errorf("未命中应返回 nil, got syn=%v rel=%v", syn2, rel2)
	}
}

// TestRewriter_NilLLM_RuleOnly 验证 nil LLM 走规则路径（同义词扩展，无纠正）。
func TestRewriter_NilLLM_RuleOnly(t *testing.T) {
	d := NewMapSynonymDict()
	d.Add("golang", []string{"go"}, []string{"rust"})
	r := NewRewriter(nil, d)

	rq, err := r.Rewrite(context.Background(), "golang")
	if err != nil {
		t.Fatalf("Rewrite 返回错误: %v", err)
	}
	if rq.Original != "golang" {
		t.Errorf("Original = %q, 期望 golang", rq.Original)
	}
	if rq.Rewritten != "golang" {
		t.Errorf("Rewritten = %q, 期望 golang（无纠正）", rq.Rewritten)
	}
	if rq.Correction != "" {
		t.Errorf("Correction = %q, 期望 空（无纠正）", rq.Correction)
	}
	if len(rq.Synonyms) != 1 || rq.Synonyms[0] != "go" {
		t.Errorf("Synonyms = %v, 期望 [go]", rq.Synonyms)
	}
	if len(rq.Related) != 1 || rq.Related[0] != "rust" {
		t.Errorf("Related = %v, 期望 [rust]", rq.Related)
	}
}

// TestRewriter_SpellingCorrection 验证编辑距离 1 拼写纠正。
func TestRewriter_SpellingCorrection(t *testing.T) {
	// 无 dict → knownWords 注入默认词（含 golang）
	r := NewRewriter(nil, nil)
	rq, err := r.Rewrite(context.Background(), "golong")
	if err != nil {
		t.Fatalf("Rewrite 返回错误: %v", err)
	}
	if rq.Rewritten != "golang" {
		t.Errorf("Rewritten = %q, 期望 golang（纠正）", rq.Rewritten)
	}
	if rq.Correction != "golang" {
		t.Errorf("Correction = %q, 期望 golang", rq.Correction)
	}
}

// TestRewriter_NoCorrection 验证已知词不纠正。
func TestRewriter_NoCorrection(t *testing.T) {
	r := NewRewriter(nil, nil)
	rq, err := r.Rewrite(context.Background(), "golang")
	if err != nil {
		t.Fatalf("Rewrite 返回错误: %v", err)
	}
	if rq.Correction != "" {
		t.Errorf("Correction = %q, 期望 空（已知词不纠正）", rq.Correction)
	}
	if rq.Rewritten != "golang" {
		t.Errorf("Rewritten = %q, 期望 golang", rq.Rewritten)
	}
}

// TestRewriter_CorrectionsMap 验证纠错词典映射。
func TestRewriter_CorrectionsMap(t *testing.T) {
	r := NewRewriter(nil, nil, WithRewriterCorrections(map[string]string{
		"pyton": "python",
	}))
	rq, err := r.Rewrite(context.Background(), "pyton")
	if err != nil {
		t.Fatalf("Rewrite 返回错误: %v", err)
	}
	if rq.Rewritten != "python" {
		t.Errorf("Rewritten = %q, 期望 python", rq.Rewritten)
	}
}

// TestRewriter_MultiTokenCorrection 验证多 token 纠正。
func TestRewriter_MultiTokenCorrection(t *testing.T) {
	r := NewRewriter(nil, nil)
	// "golong" → golang（距离 1），"rust" 已知不变
	rq, err := r.Rewrite(context.Background(), "golong rust")
	if err != nil {
		t.Fatalf("Rewrite 返回错误: %v", err)
	}
	if rq.Rewritten != "golang rust" {
		t.Errorf("Rewritten = %q, 期望 golang rust", rq.Rewritten)
	}
}

// TestRewriter_LLMPath 验证 LLM 路径并与词典合并。
func TestRewriter_LLMPath(t *testing.T) {
	d := NewMapSynonymDict()
	d.Add("golang", []string{"go", "golang"}, []string{"rust"})
	llm := &stubLLMClient{
		fn: func(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error) {
			return json.RawMessage(`{"rewritten":"golang tutorial","synonyms":["go language"],"related":[],"correction":""}`), nil
		},
	}
	r := NewRewriter(llm, d)
	rq, err := r.Rewrite(context.Background(), "golang")
	if err != nil {
		t.Fatalf("Rewrite 返回错误: %v", err)
	}
	if rq.Rewritten != "golang tutorial" {
		t.Errorf("Rewritten = %q, 期望 golang tutorial", rq.Rewritten)
	}
	// 合并 LLM 同义词 + 词典同义词（去重）
	wantSyn := map[string]bool{"go language": true, "go": true, "golang": true}
	if len(rq.Synonyms) != 3 {
		t.Errorf("Synonyms = %v, 期望 3 个去重项", rq.Synonyms)
	}
	for _, s := range rq.Synonyms {
		if !wantSyn[s] {
			t.Errorf("意外同义词 %q", s)
		}
	}
	// 词典 related 合并
	if len(rq.Related) != 1 || rq.Related[0] != "rust" {
		t.Errorf("Related = %v, 期望 [rust]", rq.Related)
	}
	if llm.calls != 1 {
		t.Errorf("LLM 调用次数 = %d, 期望 1", llm.calls)
	}
}

// TestRewriter_LLMFailFallback 验证 LLM 失败回退规则。
func TestRewriter_LLMFailFallback(t *testing.T) {
	d := NewMapSynonymDict()
	d.Add("golang", []string{"go"}, []string{"rust"})
	llm := &stubLLMClient{
		fn: func(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error) {
			return nil, errors.New("llm down")
		},
	}
	r := NewRewriter(llm, d)
	rq, err := r.Rewrite(context.Background(), "golang")
	if err != nil {
		t.Fatalf("LLM 失败应回退规则而非返回错误: %v", err)
	}
	if rq.Rewritten != "golang" {
		t.Errorf("回退规则后 Rewritten = %q, 期望 golang", rq.Rewritten)
	}
	if len(rq.Synonyms) != 1 || rq.Synonyms[0] != "go" {
		t.Errorf("回退规则后 Synonyms = %v, 期望 [go]", rq.Synonyms)
	}
}

// TestRewriter_EmptyQuery 验证空查询。
func TestRewriter_EmptyQuery(t *testing.T) {
	r := NewRewriter(nil, nil)
	rq, err := r.Rewrite(context.Background(), "")
	if err != nil {
		t.Fatalf("Rewrite 返回错误: %v", err)
	}
	if rq.Original != "" {
		t.Errorf("Original = %q, 期望 空", rq.Original)
	}
	if rq.Correction != "" {
		t.Errorf("Correction = %q, 期望 空", rq.Correction)
	}
}

// TestLevenshtein 验证编辑距离计算。
func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"", "abc", 3},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"golong", "golang", 1},
		{"flaw", "lawn", 2},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q,%q) = %d, 期望 %d", c.a, c.b, got, c.want)
		}
	}
}

// TestUnionStr 验证切片合并去重。
func TestUnionStr(t *testing.T) {
	out := unionStr([]string{"a", "b"}, []string{"b", "c"})
	if len(out) != 3 {
		t.Fatalf("union 长度 = %d, 期望 3", len(out))
	}
	want := map[string]bool{"a": true, "b": true, "c": true}
	for _, s := range out {
		if !want[s] {
			t.Errorf("意外元素 %q", s)
		}
	}
}
