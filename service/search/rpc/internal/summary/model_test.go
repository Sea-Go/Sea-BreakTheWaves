package summary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// scriptedModel 是框架 model.Model 的测试替身：按脚本返回响应流。
// 它实现的是**框架公开接口**，因此测试走的是真实装配路径。
type scriptedModel struct {
	responses []*model.Response
	err       error
	calls     int
	lastReq   *model.Request
}

func (m *scriptedModel) Info() model.Info { return model.Info{Name: "scripted"} }

func (m *scriptedModel) GenerateContent(ctx context.Context, req *model.Request) (<-chan *model.Response, error) {
	m.calls++
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan *model.Response, len(m.responses))
	for _, r := range m.responses {
		ch <- r
	}
	close(ch)
	return ch, nil
}

// textModel 返回一次性完整答案。
func textModel(answer string) *scriptedModel {
	return &scriptedModel{responses: []*model.Response{{
		Choices: []model.Choice{{Message: model.Message{Content: answer}}},
	}}}
}

// streamModel 分片返回（验证流式拼接）。
func streamModel(parts ...string) *scriptedModel {
	m := &scriptedModel{}
	for _, p := range parts {
		m.responses = append(m.responses, &model.Response{
			Choices: []model.Choice{{Delta: model.Message{Content: p}}},
		})
	}
	return m
}

// modelTestPack 构造两候选证据包（直接构造契约对象，避开需要结构树与
// 源文本的 BuildPack 路径——本测试只验证模型装配层）。
func modelTestPack(t *testing.T) evidence.EvidencePack {
	t.Helper()
	pack := evidence.EvidencePack{
		QueryID: "q-1",
		Candidates: []evidence.EvidenceCandidate{
			{
				DocKey: "doc-a", RRFScore: 0.9,
				Evidence: []evidence.Locator{{
					RevisionID:  "rev-a",
					SectionPath: []string{"海洋观测", "背景"}, ParaIndex: 0,
					Quote: "海洋观测的第 1 个要点是建立长期稳定的观测基线。",
				}},
			},
			{
				DocKey: "doc-b", RRFScore: 0.8,
				Evidence: []evidence.Locator{{
					RevisionID:  "rev-b",
					SectionPath: []string{"气候能源", "约束"}, ParaIndex: 1,
					Quote: "气候能源的第 2 个要点是量化不确定性并公开披露。",
				}},
			},
		},
	}
	if err := pack.Validate(); err != nil {
		t.Fatalf("pack fixture invalid: %v", err)
	}
	return pack
}

// TestModelSummarizerProducesValidatedResult 模型答案经框架链路产出并通过出口校验。
func TestModelSummarizerProducesValidatedResult(t *testing.T) {
	m := textModel("根据证据 [1]，海洋观测强调稳定基线 [1]；气候能源强调量化不确定性 [2]。")
	s := ModelSummarizer{Model: m}
	res, err := s.Summarize(context.Background(), SummaryRequest{
		QueryID: "q-1", Query: "海洋观测与气候能源的要点", Pack: modelTestPack(t),
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if m.calls != 1 {
		t.Fatalf("model calls = %d, want 1", m.calls)
	}
	if res.QueryID != "q-1" {
		t.Fatalf("query id = %q, want q-1", res.QueryID)
	}
	if len(res.Citations) != 2 {
		t.Fatalf("citations = %d, want 2", len(res.Citations))
	}
	if res.Citations[0].Index != 1 || res.Citations[0].DocKey != "doc-a" {
		t.Fatalf("first citation mismatch: %+v", res.Citations[0])
	}
	if res.Citations[1].Index != 2 || res.Citations[1].DocKey != "doc-b" {
		t.Fatalf("second citation mismatch: %+v", res.Citations[1])
	}
	// 提示必须携带证据编号，模型才可能产出可对账的 [n]。
	if m.lastReq == nil || len(m.lastReq.Messages) < 2 {
		t.Fatal("model request must carry system + user messages")
	}
	user := m.lastReq.Messages[1].Content
	if !strings.Contains(user, "[1]") || !strings.Contains(user, "doc-a") {
		t.Fatalf("prompt missing evidence numbering: %q", user)
	}
}

// TestModelSummarizerStreamsDeltas 流式分片被正确拼接。
func TestModelSummarizerStreamsDeltas(t *testing.T) {
	s := ModelSummarizer{Model: streamModel("根据证据 ", "[1] 与 [2]，", "两者要点不同。")}
	res, err := s.Summarize(context.Background(), SummaryRequest{
		QueryID: "q-1", Query: "要点", Pack: modelTestPack(t),
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(res.Answer, "两者要点不同") {
		t.Fatalf("streamed answer not assembled: %q", res.Answer)
	}
}

// TestModelSummarizerRejectsModelError 模型级错误向上抛，不产出答案。
func TestModelSummarizerRejectsModelError(t *testing.T) {
	s := ModelSummarizer{Model: &scriptedModel{err: errors.New("gateway unavailable")}}
	if _, err := s.Summarize(context.Background(), SummaryRequest{
		QueryID: "q-1", Query: "要点", Pack: modelTestPack(t),
	}); err == nil || !errors.Is(err, ErrModelSummary) {
		t.Fatalf("err = %v, want ErrModelSummary", err)
	}
}

// TestModelSummarizerRejectsResponseError 流内 Error 不被吞。
func TestModelSummarizerRejectsResponseError(t *testing.T) {
	m := &scriptedModel{responses: []*model.Response{
		{Error: &model.ResponseError{Message: "rate limited"}},
	}}
	if _, err := (ModelSummarizer{Model: m}).Summarize(context.Background(), SummaryRequest{
		QueryID: "q-1", Query: "要点", Pack: modelTestPack(t),
	}); err == nil || !errors.Is(err, ErrModelSummary) {
		t.Fatalf("err = %v, want ErrModelSummary", err)
	}
}

// TestModelSummarizerRejectsEmptyAnswer 空答案不被采纳。
func TestModelSummarizerRejectsEmptyAnswer(t *testing.T) {
	if _, err := (ModelSummarizer{Model: textModel("   ")}).Summarize(context.Background(), SummaryRequest{
		QueryID: "q-1", Query: "要点", Pack: modelTestPack(t),
	}); err == nil || !errors.Is(err, ErrModelSummary) {
		t.Fatalf("err = %v, want ErrModelSummary", err)
	}
}

// TestModelSummarizerRejectsAnswerWithoutCitation 模型答案缺 [n] 角标时
// 被出口校验拦下（不允许无引用答案进入交付）。
func TestModelSummarizerRejectsAnswerWithoutCitation(t *testing.T) {
	if _, err := (ModelSummarizer{Model: textModel("这是一段没有角标的答案。")}).Summarize(
		context.Background(), SummaryRequest{QueryID: "q-1", Query: "要点", Pack: modelTestPack(t)},
	); err == nil {
		t.Fatal("expected validation error for answer without [n] markers")
	}
}

// TestModelSummarizerRequiresModel 无模型即拒。
func TestModelSummarizerRequiresModel(t *testing.T) {
	if _, err := (ModelSummarizer{}).Summarize(context.Background(), SummaryRequest{
		QueryID: "q-1", Query: "要点", Pack: modelTestPack(t),
	}); err == nil || !errors.Is(err, ErrModelSummary) {
		t.Fatalf("err = %v, want ErrModelSummary", err)
	}
}

// TestModelSummarizerHonoursContextCancel ctx 取消及时返回。
func TestModelSummarizerHonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ModelSummarizer{Model: textModel("根据 [1]。")}).Summarize(ctx, SummaryRequest{
		QueryID: "q-1", Query: "要点", Pack: modelTestPack(t),
	}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

// TestModelSummarizerImplementsSummarizer 装配层实现域接口（可替换 stub）。
func TestModelSummarizerImplementsSummarizer(t *testing.T) {
	var _ Summarizer = ModelSummarizer{}
	var _ Summarizer = StubSummarizer{}
}
