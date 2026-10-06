// ============================================================================
// executor.go —— C5 步骤执行：StageFunc 接口 + 四个 dev 确定性实现。
//
// StageFunc 是单阶段执行器的统一接口（对应 C-13 阶段任务的 step 执行
// 契约 {step_id, stage, inputs_ref}——本包以 (CompileJob, Stage) 定位
// step，幂等由编排器保证）。四个 dev 执行器是 LLM 阶段的确定性替身：
// 同输入同输出、无 IO、不调模型（真实化路径见 README）。
// ============================================================================

package compile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// 执行器错误契约（errors.Is 可判别；dev 执行器与真实执行器共用）。
var (
	// ErrNoSources 标记 Curate 输入缺少资料源。
	ErrNoSources = errors.New("compile: curate 输入缺少资料源（sources 为空）")
	// ErrNoPoints 标记 Outline 输入缺少要点。
	ErrNoPoints = errors.New("compile: outline 输入缺少要点（points 为空）")
	// ErrNoOutline 标记 Article 输入缺少章节树。
	ErrNoOutline = errors.New("compile: article 输入缺少章节树（outline 为空）")
	// ErrNoArticle 标记 Polish 输入缺少全文稿。
	ErrNoArticle = errors.New("compile: polish 输入缺少全文稿（article 为空）")
	// ErrOutlineMismatch 标记章节树引用的要点下标越界或重叠。
	ErrOutlineMismatch = errors.New("compile: 章节树的要点下标与要点清单不一致")
)

// StageFunc 是单阶段执行器接口：编排器装配好 StageInput 后调用，
// 返回该阶段的 StageOutput。实现约定：
//   - 纯函数语义：只读输入，产物只经返回值交付；
//   - ctx 取消/超时应即返回 ctx.Err()；
//   - 错误即该阶段失败（编排器记账 failed，重试由调用方决定）。
//
// do_* 跳过不走本接口：编排器对可跳过阶段（仅 Polish）以 nil 执行器
// 表达（见 orchestrator.Advance）。
type StageFunc interface {
	Execute(ctx context.Context, in StageInput) (StageOutput, error)
}

// ---------------------------------------------------------------------------
// DevCurator —— Curate 阶段 dev 替身：高频词要点 + 固定视角提问。
// ---------------------------------------------------------------------------

// dev 口径常量。
const (
	// maxDevPoints Curate 产出的要点上限（高频词 top-N）。
	maxDevPoints = 8
	// minASCIIToken ASCII 词元最小长度（单字符词元噪声大，丢弃）。
	minASCIIToken = 2
)

// devPerspectives 三个固定视角问题（STORM 多视角提问的 dev 替身：
// 真实化后由 LLM 从相似主题调研动态生成 perspective 问题集）。
var devPerspectives = []string{
	"该主题的核心概念与定义是什么？",
	"该主题的实践应用与已知局限有哪些？",
	"该主题与相邻知识模块的关联和边界在哪里？",
}

// DevCurator 调研执行器 dev 替身：从资料源摘要提取高频词作为要点，
// 附 3 个固定视角问题。零值可用、无状态、并发安全、确定性。
//
// 词频口径（无分词器的确定性近似）：ASCII 连续字母/数字串（≥2 字符，
// 小写化）计一词；CJK 连续汉字串取相邻二元组（bigram）计一词。计数
// 跨全部摘要累加；top-N 按（次数降序，词元字典序升序）取，并列时
// 字典序小者优先——保证同输入同输出。
type DevCurator struct{}

// Execute 实现 StageFunc：要点=摘要高频词 top-8；多视角提问=3 个固定
// 问题；无词元可提取时报 ErrNoPoints（空摘要/纯标点输入）。
func (DevCurator) Execute(ctx context.Context, in StageInput) (StageOutput, error) {
	if err := ctx.Err(); err != nil {
		return StageOutput{}, err
	}
	if len(in.Sources) == 0 {
		return StageOutput{}, ErrNoSources
	}
	counts := make(map[string]int)
	for _, s := range in.Sources {
		for _, tok := range devTokenize(s.Summary) {
			counts[tok]++
		}
	}
	points := devTopTokens(counts, maxDevPoints)
	if len(points) == 0 {
		return StageOutput{}, fmt.Errorf("%w: 摘要中无可提取词元", ErrNoPoints)
	}
	return StageOutput{Points: points, Perspectives: append([]string(nil), devPerspectives...)}, nil
}

// devTokenize 按文件头口径切词：ASCII 词元（小写化，≥2 字符）+ CJK
// 相邻二元组；其余字符（标点/空白/全角符号）为分隔。
func devTokenize(text string) []string {
	var tokens []string
	var run []rune
	flushASCII := func() {
		if len(run) >= minASCIIToken {
			tokens = append(tokens, strings.ToLower(string(run)))
		}
		run = run[:0]
	}
	flushCJK := func() {
		for i := 0; i+1 < len(run); i++ {
			tokens = append(tokens, string(run[i:i+2]))
		}
		run = run[:0]
	}
	for _, r := range text {
		switch {
		case isASCIIWord(r):
			run = append(run, r)
		case unicode.Is(unicode.Han, r):
			run = append(run, r)
		default:
			// 分隔符：按上一个累积串的类别分别结算。
			if len(run) > 0 {
				if isASCIIWord(run[0]) {
					flushASCII()
				} else {
					flushCJK()
				}
			}
		}
	}
	if len(run) > 0 {
		if isASCIIWord(run[0]) {
			flushASCII()
		} else {
			flushCJK()
		}
	}
	return tokens
}

// isASCIIWord 报告 r 是否为 ASCII 字母/数字（词元字符）。
func isASCIIWord(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// devTopTokens 取词频 top-n（次数降序、词元字典序升序），确定性。
func devTopTokens(counts map[string]int, n int) []string {
	if len(counts) == 0 || n <= 0 {
		return nil
	}
	toks := make([]string, 0, len(counts))
	for t := range counts {
		toks = append(toks, t)
	}
	// 比较器全序（同频不同串按字典序），排序结果唯一。
	sort.Slice(toks, func(i, j int) bool {
		if counts[toks[i]] != counts[toks[j]] {
			return counts[toks[i]] > counts[toks[j]]
		}
		return toks[i] < toks[j]
	})
	if len(toks) > n {
		toks = toks[:n]
	}
	return toks
}

// ---------------------------------------------------------------------------
// DevOutliner —— Outline 阶段 dev 替身：要点均分 ≤5 章。
// ---------------------------------------------------------------------------

// maxDevChapters dev 大纲的章节数上限。
const maxDevChapters = 5

// DevOutliner 大纲执行器 dev 替身：把要点清单均分为 ≤5 章，每章标题
// 固定为"第 N 章：要点摘要"（N 从 1 起）。零值可用、确定性。
type DevOutliner struct{}

// Execute 实现 StageFunc：章节数=min(5, 要点数)，前 rem 章各多 1 个
// 要点（rem=要点数%章节数），章节按要点原序切分，PointIndexes 记录
// 各章覆盖的全局要点下标。
func (DevOutliner) Execute(ctx context.Context, in StageInput) (StageOutput, error) {
	if err := ctx.Err(); err != nil {
		return StageOutput{}, err
	}
	if len(in.Points) == 0 {
		return StageOutput{}, ErrNoPoints
	}
	chapters := len(in.Points)
	if chapters > maxDevChapters {
		chapters = maxDevChapters
	}
	base, rem := len(in.Points)/chapters, len(in.Points)%chapters
	sections := make([]Section, 0, chapters)
	pos := 0
	for i := 0; i < chapters; i++ {
		size := base
		if i < rem {
			size++
		}
		idx := make([]int, size)
		for j := 0; j < size; j++ {
			idx[j] = pos + j
		}
		pos += size
		sections = append(sections, Section{
			Title:        fmt.Sprintf("第 %d 章：要点摘要", i+1),
			PointIndexes: idx,
		})
	}
	return StageOutput{Outline: &OutlineTree{Sections: sections}}, nil
}

// ---------------------------------------------------------------------------
// DevArticleWriter —— Article 阶段 dev 替身：要点成文 + 行内 [n] 引用。
// ---------------------------------------------------------------------------

// DevArticleWriter 成文执行器 dev 替身：每章正文=对应要点逐行复述，
// 每个要点行尾追加行内 [n] 引用角标；引用映射表（引用双表第二表）
// 由 sources 按序生成：编号 n=1..len(sources) 递增，doc_key/revision
// 取自对应资料源（locator 真实化后由 B5/C-4 locator 语义补齐）。
// 正文角标按全文要点序轮转取源（第 k 个要点的角标 = k%len(sources)+1，
// 即从 1 起递增、用满源数后回卷）。零值可用、确定性。
type DevArticleWriter struct{}

// Execute 实现 StageFunc：按章节树（深度优先、文档序）生成章节稿；
// 章节树的要点下标必须严格递增且不越界（否则 ErrOutlineMismatch）。
func (DevArticleWriter) Execute(ctx context.Context, in StageInput) (StageOutput, error) {
	if err := ctx.Err(); err != nil {
		return StageOutput{}, err
	}
	if len(in.Sources) == 0 {
		return StageOutput{}, ErrNoSources
	}
	if len(in.Points) == 0 {
		return StageOutput{}, ErrNoPoints
	}
	if in.Outline == nil || len(in.Outline.Sections) == 0 {
		return StageOutput{}, ErrNoOutline
	}
	span := outlinePointSpan(in.Outline)
	if !sortedUnique(span) {
		return StageOutput{}, fmt.Errorf("%w: 下标序列 %+v", ErrOutlineMismatch, span)
	}
	for _, idx := range span {
		if idx < 0 || idx >= len(in.Points) {
			return StageOutput{}, fmt.Errorf("%w: 下标 %d 越界（要点数 %d）", ErrOutlineMismatch, idx, len(in.Points))
		}
	}
	// 引用映射表：编号→(doc_key, revision)；从 1 递增。
	citations := make([]Citation, len(in.Sources))
	for j, s := range in.Sources {
		citations[j] = Citation{Index: j + 1, DocKey: s.DocKey, RevisionID: s.RevisionID}
	}
	// 章节稿：深度优先展开章节树，逐要点行"要点 [n]"，角标轮转递增。
	var drafts []SectionDraft
	k := 0
	var walk func(secs []Section)
	walk = func(secs []Section) {
		for _, sec := range secs {
			var lines []string
			for _, idx := range sec.PointIndexes {
				n := k%len(in.Sources) + 1
				lines = append(lines, fmt.Sprintf("%s [%d]", in.Points[idx], n))
				k++
			}
			walk(sec.Children)
			drafts = append(drafts, SectionDraft{Title: sec.Title, Body: strings.Join(lines, "\n")})
		}
	}
	walk(in.Outline.Sections)
	return StageOutput{Article: &ArticleDraft{Sections: drafts, Citations: citations}}, nil
}

// ---------------------------------------------------------------------------
// DevPolisher —— Polish 阶段 dev 替身：原样返回 + 末尾声明。
// ---------------------------------------------------------------------------

// devPolishDeclaration dev 润色器附加的末尾声明（对齐平台"AI 编制→
// 人工修订"中间态：AI 产物必须显式声明待人工修订）。
const devPolishDeclaration = "由 AI 编制，待人工修订"

// DevPolisher 润色执行器 dev 替身：正文原样返回，仅在末章末尾追加
// "由 AI 编制，待人工修订"声明；不做去重（DedupNotes 为空）；引用
// 映射表原样携带。零值可用、确定性。
type DevPolisher struct{}

// Execute 实现 StageFunc：润色稿=原稿逐节复制+末尾声明；去重说明为空
// （真实化后由润色 LM 输出被合并的重复表述清单）。
func (DevPolisher) Execute(ctx context.Context, in StageInput) (StageOutput, error) {
	if err := ctx.Err(); err != nil {
		return StageOutput{}, err
	}
	if in.Article == nil || len(in.Article.Sections) == 0 {
		return StageOutput{}, ErrNoArticle
	}
	polished := &ArticleDraft{
		Sections:  make([]SectionDraft, len(in.Article.Sections)),
		Citations: append([]Citation(nil), in.Article.Citations...),
	}
	copy(polished.Sections, in.Article.Sections)
	last := len(polished.Sections) - 1
	body := polished.Sections[last].Body
	if body == "" {
		body = devPolishDeclaration
	} else {
		body = body + "\n\n" + devPolishDeclaration
	}
	polished.Sections[last].Body = body
	return StageOutput{Polished: polished}, nil
}

// DevExecutor 返回四阶段全 dev 的执行器集合（联调用；编排测试与
// worker dev 装配的默认注入）。
func DevExecutor() Executor {
	return Executor{
		Curate:  DevCurator{},
		Outline: DevOutliner{},
		Article: DevArticleWriter{},
		Polish:  DevPolisher{},
	}
}
