package quality

import (
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件定义文章质量评判的 Rubric（评分标准）体系。
//
// Rubric 是裁判 Agent（JudgeAgent）与候选 Agent（CandidateAgent）共享的
// 评判标准契约：每个 Rubric 描述一个维度（如 authority/depth/...）的评分
// 维度、判定准则（多档评分标准）与权重。RubricSet 是 Rubric 集合，支持：
//   - DefaultRubrics：构造默认 rubric 集（7 个维度，与 6 维质量分对齐 +
//     accuracy 元维度用于裁判交叉校验）
//   - Get：按维度名获取 rubric
//   - Calibrate：基于反馈数据校准 rubric 权重（离线评估）
//
// 与 domain.ArticleQuality 的对应关系：
//   - accuracy      → 裁判元维度（不直接出现在 ArticleQuality，用于交叉校验）
//   - authority     → ArticleQuality.Authority（权威性）
//   - depth         → ArticleQuality.Depth（深度）
//   - freshness     → ArticleQuality.Freshness（新鲜度）
//   - completeness  → ArticleQuality.Completeness（完整性）
//   - readability   → ArticleQuality.Readability（可读性）
//   - citation      → ArticleQuality.Citation（引用质量）
//
// 二开扩展点：业务方可实现自定义 RubricSet（如针对 IP 频道独立 rubric），
// 通过 NewCandidateAgent/NewJudgeAgent 注入；或通过 Calibrate 触发权重校准。
// ============================================================================

// 维度名常量，避免裸字符串拼写错误。
const (
	// DimensionAccuracy 准确性（裁判元维度，用于交叉校验候选评分）。
	DimensionAccuracy = "accuracy"
	// DimensionAuthority 权威性（作者/来源权威度）。
	DimensionAuthority = "authority"
	// DimensionDepth 深度（内容详尽程度）。
	DimensionDepth = "depth"
	// DimensionFreshness 新鲜度（时效性）。
	DimensionFreshness = "freshness"
	// DimensionCompleteness 完整性（结构/要素完整性）。
	DimensionCompleteness = "completeness"
	// DimensionReadability 可读性（语言流畅度/排版）。
	DimensionReadability = "readability"
	// DimensionCitation 引用质量（来源标注/可验证性）。
	DimensionCitation = "citation"
)

// Rubric 单个维度的评分标准。
//
// 由候选/裁判 Agent 注入 prompt，约束 LLM 按统一准则打分；Criteria 提供
// 多档锚点（如 1.0=优秀/0.5=一般/0.0=差），降低 LLM 评分漂移。
//
// 二开扩展点：业务方可扩展 Criteria 档位（如 5 档 1.0/0.75/0.5/0.25/0.0）
// 或自定义 Description 文案适配业务场景。
type Rubric struct {
	// Dimension 维度名（与 Dimension* 常量对齐）。
	Dimension string
	// Description 维度中文描述（注入 prompt 解释该维度评分语义）。
	Description string
	// Criteria 多档评分标准（按 Score 降序，1.0=优秀 / 0.5=一般 / 0.0=差）。
	Criteria []RubricCriterion
	// Weight 维度权重（0-1，参与 Overall 加权；RubricSet.Calibrate 校准）。
	Weight float64
}

// RubricCriterion 单档评分标准，描述该档分数对应的判定条件。
type RubricCriterion struct {
	// Score 该档分数（如 1.0 / 0.5 / 0.0）。
	Score float64
	// Description 该档中文判定条件描述。
	Description string
}

// RubricSet Rubric 集合，承载全部维度评分标准。
//
// 默认集合由 DefaultRubrics 构造（7 维）；业务方可替换为自定义集合
// （如频道独立 rubric），通过 NewCandidateAgent/NewJudgeAgent 注入。
type RubricSet struct {
	// Rubrics 维度 rubric 列表（顺序固定，便于 prompt 渲染）。
	Rubrics []Rubric
}

// DefaultRubrics 构造默认 rubric 集，包含 7 个维度：
// accuracy（裁判元维度，权重 0 用于不参与 Overall 加权）/ authority /
// depth / freshness / completeness / readability / citation。
//
// 权重默认值经业务调优得出，6 维（除 accuracy）权重和为 1.0；
// accuracy 作为裁判元维度不参与 Overall 加权（Weight=0），仅用于交叉校验。
func DefaultRubrics() *RubricSet {
	return &RubricSet{
		Rubrics: []Rubric{
			{
				Dimension:   DimensionAccuracy,
				Description: "准确性（裁判元维度）：文章事实陈述、数据、引用与常识/权威来源的一致性，用于裁判 Agent 交叉校验候选评分，不参与 Overall 加权。",
				Criteria: []RubricCriterion{
					{Score: 1.0, Description: "事实陈述全部可验证、数据准确、引用真实，与权威来源完全一致。"},
					{Score: 0.5, Description: "主体事实正确但存在细节偏差或个别引用存疑，不影响核心结论。"},
					{Score: 0.0, Description: "存在明显事实错误、数据造假或虚假引用，与权威来源矛盾。"},
				},
				Weight: 0.0,
			},
			{
				Dimension:   DimensionAuthority,
				Description: "权威性：作者/发布来源的行业权威度与专业资质，反映内容可信度。",
				Criteria: []RubricCriterion{
					{Score: 1.0, Description: "作者为领域权威专家或顶级机构，具备公认资质与高质量历史产出。"},
					{Score: 0.5, Description: "作者有一定行业经验，来源为常规主流媒体/平台，资质可查但非顶级。"},
					{Score: 0.0, Description: "作者身份不明或来源为低质匿名渠道，无资质佐证。"},
				},
				Weight: 0.20,
			},
			{
				Dimension:   DimensionDepth,
				Description: "深度：内容详尽程度与论述充分性，反映信息密度与思考深度。",
				Criteria: []RubricCriterion{
					{Score: 1.0, Description: "论述深入、论据充分、有原创观点与多角度分析，信息密度高。"},
					{Score: 0.5, Description: "覆盖主要要点但部分论据单薄，分析停留在表层。"},
					{Score: 0.0, Description: "内容空泛、信息量极低、缺乏有效论述。"},
				},
				Weight: 0.20,
			},
			{
				Dimension:   DimensionFreshness,
				Description: "新鲜度：内容时效性与信息更新程度，反映对当下场景的相关性。",
				Criteria: []RubricCriterion{
					{Score: 1.0, Description: "为最新资讯/数据（近 24h 内）或长青内容且近期复核更新。"},
					{Score: 0.5, Description: "近期内容（近 30d）但未及时更新，或长青内容无明显过时。"},
					{Score: 0.0, Description: "明显过时（如已失效的政策/价格/版本），对当下无参考价值。"},
				},
				Weight: 0.15,
			},
			{
				Dimension:   DimensionCompleteness,
				Description: "完整性：文章结构与必备要素的完整程度，反映信息闭环能力。",
				Criteria: []RubricCriterion{
					{Score: 1.0, Description: "结构完整、必备要素齐全（背景/方法/结论/限制等），形成信息闭环。"},
					{Score: 0.5, Description: "主体完整但缺失部分辅助要素（如缺限制说明/背景补充）。"},
					{Score: 0.0, Description: "结构残缺、关键要素缺失，读者无法获得完整结论。"},
				},
				Weight: 0.15,
			},
			{
				Dimension:   DimensionReadability,
				Description: "可读性：语言流畅度、排版结构与读者友好程度，反映阅读体验。",
				Criteria: []RubricCriterion{
					{Score: 1.0, Description: "语言流畅、结构清晰、排版良好，目标读者可轻松理解。"},
					{Score: 0.5, Description: "基本可读但局部晦涩或排版欠佳，需要读者额外努力。"},
					{Score: 0.0, Description: "语病频出/结构混乱/排版恶劣，难以理解。"},
				},
				Weight: 0.15,
			},
			{
				Dimension:   DimensionCitation,
				Description: "引用质量：来源标注的完整性与可验证性，反映内容可溯源性。",
				Criteria: []RubricCriterion{
					{Score: 1.0, Description: "关键论断均标注可验证来源（链接/文献/数据集），来源权威且可访问。"},
					{Score: 0.5, Description: "部分论断有引用但来源不全或可验证性一般。"},
					{Score: 0.0, Description: "无任何来源标注，或引用来源明显不可信/失效。"},
				},
				Weight: 0.15,
			},
		},
	}
}

// Get 按维度名获取 rubric。
// dimension 维度名（与 Dimension* 常量对齐）。
// 返回该维度 rubric 指针；未找到返回 nil。
func (rs *RubricSet) Get(dimension string) *Rubric {
	if rs == nil {
		return nil
	}
	for i := range rs.Rubrics {
		if rs.Rubrics[i].Dimension == dimension {
			return &rs.Rubrics[i]
		}
	}
	return nil
}

// Calibrate 基于反馈数据校准 rubric 权重。
// feedback 反馈条目列表（来自 QualityFeedback，含维度与评分）。
//
// 校准思路：统计每维度的反馈分布，对反馈持续偏低（用户认为该维度被高估）
// 的维度提升权重，反之降低；最终归一化使 6 维（除 accuracy）权重和为 1.0。
//
// TODO(离线评估): 当前为占位实现，仅校验反馈数据合法性并返回 nil。
// 后续离线评估流程将实现：
//  1. 按维度聚合 feedback.Score 均值与方差
//  2. 计算与候选 Agent 评分的偏差，得权重调整量
//  3. 应用 EMA 平滑（避免单批反馈剧烈抖动）
//  4. 归一化 6 维权重（accuracy 保持 0）
//  5. 持久化到配置/图谱 QualityReport 节点
func (rs *RubricSet) Calibrate(feedback []domain.QualityFeedback) error {
	if rs == nil {
		return fmt.Errorf("RubricSet 为 nil")
	}
	if len(feedback) == 0 {
		return nil
	}
	// TODO(离线评估): 实现基于反馈分布的权重校准逻辑。
	// 当前仅校验反馈维度合法，避免后续离线评估接入时反馈数据不合法。
	for _, fb := range feedback {
		if fb.Dimension == "" {
			continue
		}
		if rs.Get(fb.Dimension) == nil {
			return fmt.Errorf("反馈维度 %q 不在 RubricSet 中", fb.Dimension)
		}
	}
	return nil
}
