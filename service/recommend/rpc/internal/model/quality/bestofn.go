package quality

import (
	"context"
	"fmt"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// Best-of-N 编排：并行生成 N 个候选 → 裁判 → 选择最优。
// 对应 spec Task 9.4。依赖 candidate.go（CandidateAgent）与 judge.go（JudgeAgent），
// 由并行 agent（Task 9.1-9.3）创建，本文件直接复用其具体类型与 ArticleInput/JudgeResult。
// ----------------------------------------------------------------------------

// 选择模式常量。
const (
	// SelectionModePairwise 两两比较（锦标赛）选最优，对应 trpc-agent-go
	// SelectionModePairwise / llm_verifier_pairwise。
	SelectionModePairwise = "pairwise"
	// SelectionModeBest 直接选最高 Overall 分（轻量模式）。
	SelectionModeBest = "best"
)

// BestOfN Best-of-N 编排器：并行生成 N 个候选，用裁判选择最优。
//
// 二开点：
//   - 替换选择策略（SelectionModePairwise / SelectionModeBest / 自定义）
//   - 注入自研候选/裁判 Agent（替换 CandidateAgent / JudgeAgent 构造）
//   - 调整 attempts（对应 RecommendConfig.BestOfN）
type BestOfN struct {
	candidate     *CandidateAgent // 候选生成 Agent（结构化输出 6 维评分初稿）
	judge         *JudgeAgent     // 裁判 Agent（logprobs 获取 A-T 标签概率分布）
	attempts      int             // 候选生成次数（默认 3）
	selectionMode string          // 选择模式（pairwise/best）
}

// NewBestOfN 创建 Best-of-N 编排器。attempts <= 0 时默认 3。
func NewBestOfN(candidate *CandidateAgent, judge *JudgeAgent, attempts int) *BestOfN {
	if attempts <= 0 {
		attempts = 3
	}
	return &BestOfN{
		candidate:     candidate,
		judge:         judge,
		attempts:      attempts,
		selectionMode: SelectionModePairwise,
	}
}

// BestOfNResult Best-of-N 评判结果。
type BestOfNResult struct {
	Best          domain.ArticleQuality   // 最优候选质量（裁判修正后）
	AllCandidates []domain.ArticleQuality // 全部候选质量（与 JudgeResults 对齐）
	JudgeResults  []JudgeResult           // 裁判结果（与 AllCandidates 对齐）
	SelectedIndex int                     // 选中候选索引（在 AllCandidates/JudgeResults 中）
}

// Evaluate 执行 Best-of-N 评判：
//  1. 并行调 candidate.Evaluate N 次（goroutine）生成候选
//  2. 用 judge.Judge 裁判每个候选（传入文章 + 候选评分初稿）
//  3. 按 selectionMode 选择最优（SelectionModePairwise 两两比较选最优）
//  4. 返回最高质量结果
func (b *BestOfN) Evaluate(ctx context.Context, article ArticleInput) (BestOfNResult, error) {
	n := b.attempts

	// 并行生成 N 个候选（按索引写入数组，保证顺序确定）。
	rawCandidates := make([]domain.ArticleQuality, n)
	rawErrs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			q, err := b.candidate.Evaluate(ctx, article)
			rawCandidates[idx] = q
			rawErrs[idx] = err
		}(i)
	}
	wg.Wait()

	// 至少一个候选成功即可继续；全部失败返回错误。
	successCount := 0
	for _, err := range rawErrs {
		if err == nil {
			successCount++
		}
	}
	if successCount == 0 {
		return BestOfNResult{}, fmt.Errorf("bestofn: 全部候选生成失败: %w", rawErrs[0])
	}

	// 顺序裁判每个成功候选（保持与 AllCandidates 索引对齐）。
	// JudgeAgent.Judge 接收文章 + 候选评分初稿，返回含 A-T 概率分布的 JudgeResult。
	allCandidates := make([]domain.ArticleQuality, 0, successCount)
	judgeResults := make([]JudgeResult, 0, successCount)
	for i := 0; i < n; i++ {
		if rawErrs[i] != nil {
			continue
		}
		allCandidates = append(allCandidates, rawCandidates[i])
		jr, jerr := b.judge.Judge(ctx, article, rawCandidates[i])
		if jerr != nil {
			// 裁判失败：降级用原候选质量，置信度置 0。
			jr = JudgeResult{
				Quality:    rawCandidates[i],
				LabelProbs: nil,
				Confidence: 0,
			}
		}
		judgeResults = append(judgeResults, jr)
	}

	// 选择最优候选。
	selectedIndex := b.selectBest(judgeResults)

	return BestOfNResult{
		Best:          judgeResults[selectedIndex].Quality,
		AllCandidates: allCandidates,
		JudgeResults:  judgeResults,
		SelectedIndex: selectedIndex,
	}, nil
}

// selectBest 按选择模式选出最优候选索引。
func (b *BestOfN) selectBest(results []JudgeResult) int {
	if len(results) == 0 {
		return 0
	}
	best := 0
	if b.selectionMode == SelectionModePairwise {
		// 两两比较（锦标赛）：遍历比较，胜者成为新最优。
		for i := 1; i < len(results); i++ {
			if b.pairwiseCompare(results[i].Quality, results[best].Quality) > 0 {
				best = i
			}
		}
	} else {
		// SelectionModeBest：直接选最高 Overall。
		for i := 1; i < len(results); i++ {
			if results[i].Quality.Overall > results[best].Quality.Overall {
				best = i
			}
		}
	}
	return best
}

// pairwiseCompare 两两比较两个候选质量。
// 返回值：1 表示 a 优 / 0 平局 / -1 表示 a 劣。
//
// 比较规则：6 维逐维比较，胜出维度多者优；维度持平则比 Overall。
//
// 二开点：可替换为基于 LLM Verifier 的概率分布比较（llm_verifier_pairwise），
// 或加权维度比较（业务自定义权重）；当前为维度多数表决 + Overall 兜底。
func (b *BestOfN) pairwiseCompare(a, c domain.ArticleQuality) int {
	aWins, cWins := 0, 0
	dims := []struct{ av, cv float64 }{
		{a.Authority, c.Authority},
		{a.Depth, c.Depth},
		{a.Freshness, c.Freshness},
		{a.Completeness, c.Completeness},
		{a.Readability, c.Readability},
		{a.Citation, c.Citation},
	}
	for _, d := range dims {
		if d.av > d.cv {
			aWins++
		} else if d.av < d.cv {
			cWins++
		}
	}
	if aWins > cWins {
		return 1
	}
	if aWins < cWins {
		return -1
	}
	// 维度持平，比 Overall 综合分。
	if a.Overall > c.Overall {
		return 1
	}
	if a.Overall < c.Overall {
		return -1
	}
	return 0
}
