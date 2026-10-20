// ============================================================================
// stage.go —— C4/C5 编制流水线的阶段契约（工程方案 §4.8 决策 8）。
//
// §4.8：编制流水线采 STORM 四阶段〔依据：STORM 代码级核验——Knowledge
// Curation→Outline Generation→Article Generation→Article Polishing〕，
// 每阶段产物落盘 + do_* 开关断点重跑。本文件把四阶段与各阶段的输入/
// 输出数据契约落成 Go 类型：Stage 是阶段枚举，StageInput/StageOutput 是
// 跨阶段传递的纯数据（JSON snake_case，供 worker 落盘与断点恢复）。
// 阶段的执行在 executor.go（C5），阶段的编排在 orchestrator.go（C4）。
// ============================================================================

package compile

import (
	"fmt"
	"strings"
)

// Stage 编制流水线阶段：STORM 四模块的 Sea 形态。
// 值即跨层传输与落盘的稳定标识（C-13 阶段任务契约中的 stage 字段）。
type Stage string

const (
	// StageCurate Knowledge Curation：调研资料源，产要点清单+多视角提问。
	StageCurate Stage = "curate"
	// StageOutline Outline Generation：由要点生成章节树。
	StageOutline Stage = "outline"
	// StageArticle Article Generation：按章节+证据生成正文（行内 [n] 引用）。
	StageArticle Stage = "article"
	// StagePolish Article Polishing：润色全文+去重说明；唯一可跳过的阶段。
	StagePolish Stage = "polish"
)

// Stages 返回按执行序排列的四阶段（Curate→Outline→Article→Polish）。
func Stages() []Stage {
	return []Stage{StageCurate, StageOutline, StageArticle, StagePolish}
}

// Valid 报告 s 是否为合法阶段值。
func (s Stage) Valid() bool {
	switch s {
	case StageCurate, StageOutline, StageArticle, StagePolish:
		return true
	default:
		return false
	}
}

// String 实现 Stringer（Stage 本身即是字符串，仅为接口完整）。
func (s Stage) String() string {
	return string(s)
}

// order 返回阶段的执行序（curate=1..polish=4；未知阶段 0）。
// 前置校验（prerequisite）与状态推进均按该序判定。
func (s Stage) order() int {
	switch s {
	case StageCurate:
		return 1
	case StageOutline:
		return 2
	case StageArticle:
		return 3
	case StagePolish:
		return 4
	default:
		return 0
	}
}

// prerequisite 返回 stage 的前置阶段与是否存在（curate 无前置）。
func (s Stage) prerequisite() (Stage, bool) {
	switch s {
	case StageOutline:
		return StageCurate, true
	case StageArticle:
		return StageOutline, true
	case StagePolish:
		return StageArticle, true
	default:
		return "", false
	}
}

// SourceRef 是 Curate 阶段的资料源引用：doc_key+摘要（+冻结修订 ID）。
// 引用双表的"编号→(doc revision, locator)"以 RevisionID 为 revision 侧
// 锚点（管理员导入语料已冻结修订；dev 执行器允许为空）。
type SourceRef struct {
	// DocKey 资料源文档键（检索层稳定文档标识，全链路唯一）。
	DocKey string `json:"doc_key"`
	// Summary 资料源摘要（Curate 提取要点的文本来源）。
	Summary string `json:"summary"`
	// RevisionID 资料源冻结修订 ID（引用双表 revision 侧；可空=dev）。
	RevisionID string `json:"revision_id,omitempty"`
}

// Section 是章节树节点。树形契约（Children）为真实化后的层级大纲预留；
// dev 大纲器只产平铺一层（≤5 章）。
type Section struct {
	// Title 章节标题（dev 口径："第 N 章：要点摘要"）。
	Title string `json:"title"`
	// PointIndexes 本章节覆盖的要点序号（指向 Curate 输出的 Points 下标）。
	PointIndexes []int `json:"point_indexes,omitempty"`
	// Children 子章节（层级大纲预留；dev 为空）。
	Children []Section `json:"children,omitempty"`
}

// OutlineTree 是 Outline 阶段产出的章节树（当前为根下一层的平铺序列）。
type OutlineTree struct {
	// Sections 根下的章节列表（≥1 章）。
	Sections []Section `json:"sections"`
}

// SectionDraft 是成文后的章节：标题+正文（正文含行内 [n] 引用角标）。
type SectionDraft struct {
	// Title 章节标题（与大纲 Section.Title 一致）。
	Title string `json:"title"`
	// Body 章节正文；行内 [n] 角标的 n 对应 Citation.Index（引用双表）。
	Body string `json:"body"`
}

// ArticleDraft 是 Article/Polish 阶段的全文形态：章节稿序列+引用映射表。
// 引用双表=正文行内 [n]（各 SectionDraft.Body 内）+ 本结构 Citations
// 独立映射表（编号→doc revision/locator，见 claim.go 的 Citation）。
type ArticleDraft struct {
	// Sections 章节稿列表（与大纲章节一一对应）。
	Sections []SectionDraft `json:"sections"`
	// Citations 引用映射表（编号→(doc revision, locator)）；由 Article
	// 阶段生成、Polish 阶段原样携带。编号从 1 递增。
	Citations []Citation `json:"citations"`
}

// FullText 返回全文的纯文本形态：逐章节"标题行+正文"以换行拼接。
// 供 Polish 输入口径、Claim 提取与前端预览使用。
func (a *ArticleDraft) FullText() string {
	if a == nil {
		return ""
	}
	lines := make([]string, 0, 2*len(a.Sections))
	for _, s := range a.Sections {
		lines = append(lines, s.Title)
		if s.Body != "" {
			lines = append(lines, s.Body)
		}
	}
	return strings.Join(lines, "\n")
}

// StageInput 是单阶段执行器（StageFunc）的输入。字段按阶段取用：
// Curate 用 Sources；Outline 用 Points；Article 用 Sources+Points+
// Outline；Polish 用 Article。编排器负责从既有阶段产物装配（见
// orchestrator.stageInput），执行器不得假设未列出字段非空。
type StageInput struct {
	// Sources 资料源列表（Curate 输入；Article 引用表取 doc_key/revision）。
	Sources []SourceRef `json:"sources,omitempty"`
	// Points 要点清单（Curate 输出→Outline/Article 输入）。
	Points []string `json:"points,omitempty"`
	// Outline 章节树（Outline 输出→Article 输入）。
	Outline *OutlineTree `json:"outline,omitempty"`
	// Article 全文章节稿（Article 输出→Polish 输入）。
	Article *ArticleDraft `json:"article,omitempty"`
}

// StageOutput 是单阶段执行器的输出。各阶段只填契约字段：
//
//	Curate  → Points + Perspectives
//	Outline → Outline
//	Article → Article（含 Citations 引用映射表）
//	Polish  → Polished + DedupNotes
//
// 未涉字段为零值；编排器按阶段校验必填字段后存入 StageState.Output。
type StageOutput struct {
	// Points 要点清单（Curate）。
	Points []string `json:"points,omitempty"`
	// Perspectives 多视角提问（Curate；STORM perspective questioning 的
	// dev 替身，真实化后为 LLM 调研问题集）。
	Perspectives []string `json:"perspectives,omitempty"`
	// Outline 章节树（Outline）。
	Outline *OutlineTree `json:"outline,omitempty"`
	// Article 带行内引用的全文章节稿（Article）。
	Article *ArticleDraft `json:"article,omitempty"`
	// Polished 润色后全文章节稿（Polish）。
	Polished *ArticleDraft `json:"polished,omitempty"`
	// DedupNotes 去重说明（Polish；记录被合并/删除的重复表述，供审计；
	// dev 润色器不去重故为空）。
	DedupNotes []string `json:"dedup_notes,omitempty"`
}

// validateOutput 校验 stage 的输出是否满足出口契约（编排器在采纳输出前
// 调用；防止执行器返回空产物导致后续阶段装配失败）。
func (s Stage) validateOutput(out StageOutput) error {
	switch s {
	case StageCurate:
		if len(out.Points) == 0 {
			return fmt.Errorf("compile: curate 输出缺少要点清单（points 为空）")
		}
		if len(out.Perspectives) == 0 {
			return fmt.Errorf("compile: curate 输出缺少多视角提问（perspectives 为空）")
		}
	case StageOutline:
		if out.Outline == nil || len(out.Outline.Sections) == 0 {
			return fmt.Errorf("compile: outline 输出缺少章节树（outline 为空）")
		}
	case StageArticle:
		if out.Article == nil || len(out.Article.Sections) == 0 {
			return fmt.Errorf("compile: article 输出缺少章节稿（article 为空）")
		}
		if len(out.Article.Citations) == 0 {
			return fmt.Errorf("compile: article 输出缺少引用映射表（citations 为空）")
		}
	case StagePolish:
		if out.Polished == nil || len(out.Polished.Sections) == 0 {
			return fmt.Errorf("compile: polish 输出缺少润色稿（polished 为空）")
		}
	default:
		return fmt.Errorf("compile: 未知编制阶段 %q", s)
	}
	return nil
}

// outlinePointSpan 展开章节树覆盖的要点下标（深度优先、按文档序）。
// dev 大纲只一层；层级大纲真实化后同样按先序展开。
func outlinePointSpan(t *OutlineTree) []int {
	var span []int
	var walk func(secs []Section)
	walk = func(secs []Section) {
		for _, sec := range secs {
			span = append(span, sec.PointIndexes...)
			walk(sec.Children)
		}
	}
	if t != nil {
		walk(t.Sections)
	}
	return span
}

// sortedUnique 报告 span 是否严格递增（章节间要点不重叠且有序）。
// dev 均分大纲满足；用于 Article 阶段的大纲一致性护栏。
func sortedUnique(span []int) bool {
	seen := make(map[int]bool, len(span))
	prev := -1
	for _, v := range span {
		if v <= prev || seen[v] {
			return false
		}
		seen[v] = true
		prev = v
	}
	return true
}
