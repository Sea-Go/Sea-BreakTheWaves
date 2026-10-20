// model.go —— B6 摘要的 tRPC-Agent-Go 模型装配层（C17 + C31）。
//
// 职责边界：
//   - 本文件是**框架装配层**：用框架公开的 model.Model 接口驱动摘要生成，
//     经框架的消息/响应类型与调用约定，替代 stub.go 的确定性替身；
//   - summary.go 的契约类型、validate.go 的出口校验、stub.go 的
//     resolveQueryID/topCandidates/truncateRunes 均被复用，本层不重复实现；
//   - 模型来源由调用方注入（C31：模型调用收敛到 DC 网关），本层不选模型、
//     不持有密钥、不直连 provider。
//
// 与 stub 的关系：StubSummarizer 是无模型的确定性替身（dev/测试用）；
// ModelSummarizer 是经框架模型的真实路径。两者实现同一 Summarizer 接口，
// 出口校验完全相同——模型输出必须先过 Validate 才可能被采纳。
package summary

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// ErrModelSummary 标记框架模型调用失败或输出不可用。
var ErrModelSummary = errors.New("summary: model call failed")

// DefaultSummaryInstruction 是摘要交付的系统提示。约束与域层红线一致：
// 只用给定证据、按序编号、缺证据就说明缺口（不编造）。
const DefaultSummaryInstruction = "你是知识问答的摘要交付组件。" +
	"只能使用给定证据片段作答，不得引入外部知识或编造事实。" +
	"引用证据时用 [n] 标注，n 与证据序号一致。" +
	"证据不足时明确说明缺口，不要猜测。"

// ModelSummarizer 经框架 model.Model 生成答案，并复用域层出口校验。
type ModelSummarizer struct {
	// Model 是框架模型（C31：由 DC 网关侧注入，含限额与候选切换）。
	Model model.Model
	// Instruction 是系统提示；空则用 DefaultSummaryInstruction。
	Instruction string
	// MaxEvidence 进入提示的候选文档数上限（<=0 取 StubTopCandidates）。
	MaxEvidence int
}

// Summarize 组装提示、经框架模型生成答案，再抽取引用并做出口校验。
// 模型输出不能被 validate.go 接受时返回错误而非放行。
func (m ModelSummarizer) Summarize(ctx context.Context, req SummaryRequest) (SummaryResult, error) {
	if m.Model == nil {
		return SummaryResult{}, fmt.Errorf("%w: model is required", ErrModelSummary)
	}
	if err := ctx.Err(); err != nil {
		return SummaryResult{}, fmt.Errorf("summary: 已取消: %w", err)
	}
	qid, err := resolveQueryID(req)
	if err != nil {
		return SummaryResult{}, err
	}
	if err := req.Pack.Validate(); err != nil {
		return SummaryResult{}, fmt.Errorf("summary: 入口校验证据包失败: %w", err)
	}
	if len(req.Pack.Candidates) == 0 {
		return SummaryResult{}, fmt.Errorf("summary: 证据包无候选，拒绝产出无证据摘要")
	}

	limit := m.MaxEvidence
	if limit <= 0 {
		limit = StubTopCandidates
	}
	top := topCandidates(req.Pack.Candidates, limit)
	if len(top) == 0 {
		return SummaryResult{}, fmt.Errorf("%w: no evidence candidates", ErrModelSummary)
	}

	instruction := m.Instruction
	if instruction == "" {
		instruction = DefaultSummaryInstruction
	}
	request := &model.Request{
		Messages: []model.Message{
			model.NewSystemMessage(instruction),
			model.NewUserMessage(buildPrompt(req.Query, top)),
		},
	}
	stream, err := m.Model.GenerateContent(ctx, request)
	if err != nil {
		return SummaryResult{}, fmt.Errorf("%w: %v", ErrModelSummary, err)
	}
	answer, err := collectAnswer(ctx, stream)
	if err != nil {
		return SummaryResult{}, err
	}

	// 引用沿用域层口径：取每条候选的第一条 Locator（与 stub 一致），
	// quote 再按 evidence.MaxQuoteRunes 截断做双保险。
	citations := make([]Citation, 0, len(top))
	for i, c := range top {
		loc := c.Evidence[0]
		loc.Quote = truncateRunes(loc.Quote, evidence.MaxQuoteRunes)
		citations = append(citations, Citation{Index: i + 1, DocKey: c.DocKey, Locator: loc})
	}
	result := SummaryResult{QueryID: qid, Answer: answer, Citations: citations}
	if err := result.Validate(); err != nil {
		return SummaryResult{}, fmt.Errorf("summary: 模型答案未过出口校验: %w", err)
	}
	return result, nil
}

// buildPrompt 把查询与候选证据拼成用户消息。证据按序编号，编号即 [n] 角标。
func buildPrompt(query string, candidates []evidence.EvidenceCandidate) string {
	var b strings.Builder
	b.WriteString("问题：")
	b.WriteString(strings.TrimSpace(query))
	b.WriteString("\n\n证据片段：\n")
	for i, c := range candidates {
		loc := c.Evidence[0]
		fmt.Fprintf(&b, "[%d] 文档 %s", i+1, c.DocKey)
		if path := strings.Join(loc.SectionPath, " › "); path != "" {
			fmt.Fprintf(&b, "（%s ¶%d）", path, loc.ParaIndex)
		}
		b.WriteString("\n    摘录：")
		b.WriteString(truncateRunes(loc.Quote, evidence.MaxQuoteRunes))
		b.WriteString("\n")
	}
	b.WriteString("\n请基于以上证据作答，用 [n] 标注引用。")
	return b.String()
}

// collectAnswer 消费框架响应流并拼出最终答案。流中的 Error 一律向上抛，
// 不静默吞掉——否则会把失败当成功答案写进引用。
func collectAnswer(ctx context.Context, stream <-chan *model.Response) (string, error) {
	if stream == nil {
		return "", fmt.Errorf("%w: nil response stream", ErrModelSummary)
	}
	var b strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("summary: 已取消: %w", ctx.Err())
		case resp, ok := <-stream:
			if !ok {
				answer := strings.TrimSpace(b.String())
				if answer == "" {
					return "", fmt.Errorf("%w: empty model answer", ErrModelSummary)
				}
				return answer, nil
			}
			if resp == nil {
				continue
			}
			if resp.Error != nil {
				return "", fmt.Errorf("%w: %s", ErrModelSummary, resp.Error.Message)
			}
			for _, choice := range resp.Choices {
				if text := choice.Message.Content; text != "" {
					b.WriteString(text)
				}
				if text := choice.Delta.Content; text != "" {
					b.WriteString(text)
				}
			}
		}
	}
}
