// ============================================================================
// orchestrator.go —— C4 编制编排状态机（工程方案 §4.8 决策 8）。
//
// CompileJob 携带四阶段产物（Stages map），Advance 是唯一的推进原语：
//   - 幂等：已完成阶段直接短路返回（对应 STORM"产物已落盘则跳过"）；
//   - 失败不回退：阶段失败只置 failed+StageState.Err，已完成阶段原样
//     保留，对同阶段再次 Advance 即重试（断点续跑）；
//   - do_* 语义：execute 传 nil 表示跳过该阶段——仅 Polish 允许
//     （do_polish_article=false），Curate/Outline/Article 是内容链前置，
//     不可跳过。
//
// 本文件是纯域层：无 IO、无时钟依赖注入（用 time.Now UTC 记账）、
// 同输入同输出；产物落盘由 worker 装配层按 JSON 序列化承接。
// ============================================================================

package compile

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// JobStatus 编制任务状态。推进链：
// pending→curating→outlining→articling→polishing→done；任一阶段失败
// →failed（记录在 StageState.Err，重试该阶段即从 failed 恢复）。
// status 语义=「当前正在执行、或已完成待下一阶段」的时刻；阶段完成
// 与否的权威记录在 Stages[stage].Completed。
type JobStatus string

const (
	// StatusPending 初始态：尚未执行任何阶段。
	StatusPending JobStatus = "pending"
	// StatusCurating Curate 进行中或已完成（待 Outline）。
	StatusCurating JobStatus = "curating"
	// StatusOutlining Outline 进行中或已完成（待 Article）。
	StatusOutlining JobStatus = "outlining"
	// StatusArticling Article 进行中或已完成（待 Polish 或显式跳过）。
	StatusArticling JobStatus = "articling"
	// StatusPolishing Polish 进行中或已完成（待 done）。
	StatusPolishing JobStatus = "polishing"
	// StatusDone 全部启用阶段完成（Polish 完成或被显式跳过）。
	StatusDone JobStatus = "done"
	// StatusFailed 某阶段失败；StageState.Err 记录原因，重试即恢复。
	StatusFailed JobStatus = "failed"
)

// 编排错误契约（errors.Is 可判别）。
var (
	// ErrInvalidStage 标记未知阶段值。
	ErrInvalidStage = errors.New("compile: 未知编制阶段")
	// ErrStageOrder 标记前置阶段未完成即请求后续阶段。
	ErrStageOrder = errors.New("compile: 前置阶段未完成")
	// ErrStageInput 标记阶段输入装配失败（前置产物缺失或为空）。
	ErrStageInput = errors.New("compile: 阶段输入未装配")
	// ErrSkipNotAllowed 标记对不可跳过阶段传 nil 执行器（仅 Polish 可跳过）。
	ErrSkipNotAllowed = errors.New("compile: 该阶段不可跳过（仅 polish 可跳过）")
	// ErrEmptyJob 标记编制任务基础字段不完整（job_id/module_id/sources）。
	ErrEmptyJob = errors.New("compile: 编制任务字段不完整")
)

// StageState 是单阶段的执行记账：启动/完成时间、产物与失败原因。
// Completed 为零值表示未完成（进行中或失败）；Err 非空表示最近一次
// 执行失败（重试成功后清空）。
type StageState struct {
	// Started 本阶段最近一次执行的启动时间（UTC）。
	Started time.Time `json:"started"`
	// Completed 本阶段完成时间（UTC）；零值=未完成。
	Completed time.Time `json:"completed"`
	// Output 本阶段产物（完成后非空；幂等跳过与断点恢复的数据源）。
	Output *StageOutput `json:"output,omitempty"`
	// Err 本阶段最近一次失败的原因；成功后为空。
	Err string `json:"err,omitempty"`
}

// done 报告该阶段是否已完成（有产物）。
func (s *StageState) done() bool {
	return s != nil && !s.Completed.IsZero() && s.Output != nil
}

// CompileJob 是一次 Wiki 编制任务（C4 编排的对象形态；JSON snake_case
// 供 worker 落盘与断点恢复）。值类型使用：Advance 不改写传入值，而是
// 返回推进后的新 CompileJob（stages map 深拷贝），调用方以返回值续跑。
type CompileJob struct {
	// JobID 编制任务 ID（C-13 阶段任务的 job 侧标识；非空）。
	JobID string `json:"job_id"`
	// ModuleID 归属知识模块 ID（非空；发布门禁在 A5）。
	ModuleID string `json:"module_id"`
	// Sources 资料源列表（Curate 输入；Article 引用表的 doc_key 来源）。
	Sources []SourceRef `json:"sources"`
	// Status 当前状态（见 JobStatus 推进链）。
	Status JobStatus `json:"status"`
	// Stages 各阶段记账（按 Stage 键存；未执行的阶段无键）。
	Stages map[Stage]*StageState `json:"stages"`
}

// NewCompileJob 构造初始任务：校验 job_id/module_id/sources 非空且
// doc_key 均非空，状态 pending、空阶段表。
func NewCompileJob(jobID, moduleID string, sources []SourceRef) (CompileJob, error) {
	if jobID == "" || moduleID == "" {
		return CompileJob{}, fmt.Errorf("%w: job_id 与 module_id 均不能为空", ErrEmptyJob)
	}
	if len(sources) == 0 {
		return CompileJob{}, fmt.Errorf("%w: sources 不能为空", ErrEmptyJob)
	}
	for i, s := range sources {
		if s.DocKey == "" {
			return CompileJob{}, fmt.Errorf("%w: sources[%d].doc_key 为空", ErrEmptyJob, i)
		}
	}
	return CompileJob{
		JobID:    jobID,
		ModuleID: moduleID,
		Sources:  append([]SourceRef(nil), sources...),
		Status:   StatusPending,
		Stages:   make(map[Stage]*StageState),
	}, nil
}

// Clone 深拷贝任务（Sources 与 Stages 均复制；Output 指针共享——产物
// 一经采纳即视为不可变）。Advance 以副本推进，保证调用方持有的原值
// 不被改写（失败重试"job 不变"的语义基础）。
func (j CompileJob) Clone() CompileJob {
	c := j
	c.Sources = append([]SourceRef(nil), j.Sources...)
	if j.Stages == nil {
		c.Stages = make(map[Stage]*StageState)
		return c
	}
	c.Stages = make(map[Stage]*StageState, len(j.Stages))
	for st, state := range j.Stages {
		cp := *state
		c.Stages[st] = &cp
	}
	return c
}

// Output 返回指定阶段的产物；阶段未完成时为 nil。
func (j CompileJob) Output(stage Stage) *StageOutput {
	if s := j.Stages[stage]; s.done() {
		return s.Output
	}
	return nil
}

// FinalArticle 返回终稿：Polish 产物优先，否则 Article 产物；两者皆无
// （未成文）时为 nil。发布链（A5）与本包边界在此交接。
func (j CompileJob) FinalArticle() *ArticleDraft {
	if p := j.Output(StagePolish); p != nil && p.Polished != nil {
		return p.Polished
	}
	if a := j.Output(StageArticle); a != nil {
		return a.Article
	}
	return nil
}

// Orchestrator 编制编排器：零值可用、无状态、并发安全。
type Orchestrator struct{}

// NewOrchestrator 返回一个编排器（等价零值，仅为显式构造习惯保留）。
func NewOrchestrator() *Orchestrator {
	return &Orchestrator{}
}

// Advance 推进一个阶段——C4 的唯一推进原语，语义：
//
//  1. 幂等：stage 已完成（有产物）→ 原样返回 job，不调执行器
//     （STORM"产物已存在则跳过该 do_* 段"的 Sea 形态）；
//  2. 前置校验：前置阶段未完成 → ErrStageOrder，job 原样返回；
//  3. do_* 跳过：execute 为 nil → 跳过该阶段；仅 Polish 允许
//     （do_polish=false），其余返回 ErrSkipNotAllowed；
//  4. 执行：装配 StageInput（从既有阶段产物推导），调 execute；
//     失败 → 返回 failed 任务（该阶段 Err 记账、已完成阶段不动，
//     即"失败不回退"），对同阶段再调 Advance 即重试；
//     成功 → 校验出口契约后存产物，状态推进到下一时刻。
//
// 返回的 job 与传入值不共享 stages map；调用方以返回值续跑。
func (o *Orchestrator) Advance(ctx context.Context, job CompileJob, stage Stage, execute StageFunc) (CompileJob, error) {
	if !stage.Valid() {
		return job, fmt.Errorf("%w: %q（合法值 curate|outline|article|polish）", ErrInvalidStage, stage)
	}
	// 幂等短路：已完成阶段直接跳过。
	if job.Stages[stage].done() {
		return job, nil
	}
	// 前置校验（失败不回退的前提：已完成阶段的记账只增不减）。
	if prev, ok := stage.prerequisite(); ok {
		if !job.Stages[prev].done() {
			return job, fmt.Errorf("%w: %s 需要 %s 先完成", ErrStageOrder, stage, prev)
		}
	}
	// do_* 跳过语义：nil 执行器=do_polish=false。
	if execute == nil {
		if stage != StagePolish {
			return job, fmt.Errorf("%w: %s", ErrSkipNotAllowed, stage)
		}
		next := job.Clone()
		next.Status = StatusDone
		return next, nil
	}
	in, err := o.stageInput(job, stage)
	if err != nil {
		return job, err
	}
	next := job.Clone()
	state := &StageState{Started: time.Now().UTC()}
	next.Stages[stage] = state
	next.Status = runningStatus(stage)
	out, execErr := execute.Execute(ctx, in)
	if execErr == nil {
		execErr = stage.validateOutput(out)
	}
	if execErr != nil {
		state.Err = execErr.Error()
		next.Status = StatusFailed
		return next, fmt.Errorf("compile: %s 阶段执行失败: %w", stage, execErr)
	}
	state.Completed = time.Now().UTC()
	state.Output = &out
	next.Status = completionStatus(stage)
	return next, nil
}

// stageInput 按阶段装配执行器输入（C4 的阶段间接线）：
// curate←job.Sources；outline←curate.Points；article←sources+points+
// outline；polish←article.Article。
func (o *Orchestrator) stageInput(job CompileJob, stage Stage) (StageInput, error) {
	switch stage {
	case StageCurate:
		if len(job.Sources) == 0 {
			return StageInput{}, fmt.Errorf("%w: curate 需要 sources", ErrStageInput)
		}
		return StageInput{Sources: job.Sources}, nil
	case StageOutline:
		c := job.Output(StageCurate)
		if c == nil || len(c.Points) == 0 {
			return StageInput{}, fmt.Errorf("%w: outline 需要 curate 的要点清单", ErrStageInput)
		}
		return StageInput{Points: c.Points}, nil
	case StageArticle:
		c, ol := job.Output(StageCurate), job.Output(StageOutline)
		if c == nil || len(c.Points) == 0 {
			return StageInput{}, fmt.Errorf("%w: article 需要 curate 的要点清单", ErrStageInput)
		}
		if ol == nil || ol.Outline == nil {
			return StageInput{}, fmt.Errorf("%w: article 需要 outline 的章节树", ErrStageInput)
		}
		if len(job.Sources) == 0 {
			return StageInput{}, fmt.Errorf("%w: article 需要 sources 构造引用表", ErrStageInput)
		}
		return StageInput{Sources: job.Sources, Points: c.Points, Outline: ol.Outline}, nil
	case StagePolish:
		a := job.Output(StageArticle)
		if a == nil || a.Article == nil {
			return StageInput{}, fmt.Errorf("%w: polish 需要 article 的全文稿", ErrStageInput)
		}
		return StageInput{Article: a.Article}, nil
	default:
		return StageInput{}, fmt.Errorf("%w: %q", ErrInvalidStage, stage)
	}
}

// runningStatus 返回阶段执行中的状态值。
func runningStatus(stage Stage) JobStatus {
	switch stage {
	case StageCurate:
		return StatusCurating
	case StageOutline:
		return StatusOutlining
	case StageArticle:
		return StatusArticling
	case StagePolish:
		return StatusPolishing
	default:
		return StatusPending
	}
}

// completionStatus 返回阶段成功后的状态：推进到下一时刻；polish 完成
// 即 done。
func completionStatus(stage Stage) JobStatus {
	switch stage {
	case StageCurate:
		return StatusOutlining
	case StageOutline:
		return StatusArticling
	case StageArticle:
		return StatusPolishing
	case StagePolish:
		return StatusDone
	default:
		return StatusPending
	}
}

// RunOptions 是 Run 的 do_* 开关集合。零值=四阶段全跑（STORM 默认
// do_research/do_generate_outline/do_generate_article/do_polish_article
// 全 true 的 Sea 形态）；SkipPolish 对应 do_polish_article=false。
type RunOptions struct {
	// SkipPolish 跳过润色阶段（Article 终稿直接 done）。
	SkipPolish bool `json:"skip_polish"`
}

// Executor 是一次 Run 的四阶段执行器集合（C5 步骤执行的注入点）；
// 每个字段实现 StageFunc，可混搭 dev 与真实 LLM 执行器。
type Executor struct {
	// Curate 调研执行器（要点+多视角提问）。
	Curate StageFunc
	// Outline 大纲执行器。
	Outline StageFunc
	// Article 成文执行器。
	Article StageFunc
	// Polish 润色执行器（SkipPolish 时可不填）。
	Polish StageFunc
}

// StageFunc 按阶段返回对应执行器（未配置的阶段返回 nil）。装配层（含
// GraphAgent 节点）用它按阶段取执行器，无需各自维护一份 switch。
func (e Executor) StageFunc(stage Stage) StageFunc {
	switch stage {
	case StageCurate:
		return e.Curate
	case StageOutline:
		return e.Outline
	case StageArticle:
		return e.Article
	case StagePolish:
		return e.Polish
	default:
		return nil
	}
}

// Run 按序执行四阶段（do_* 断点续跑的编排入口）：逐阶段 Advance，
// 任一阶段失败即返回当次 job 与错误——已完成阶段已记账，重入 Run
// （或逐阶段 Advance）自动从断点续跑；SkipPolish=true 时以 nil 执行
// 器显式跳过润色（do_polish=false）。
func (o *Orchestrator) Run(ctx context.Context, job CompileJob, opts RunOptions, ex Executor) (CompileJob, error) {
	var err error
	for _, stage := range Stages() {
		var fn StageFunc
		switch stage {
		case StageCurate:
			fn = ex.Curate
		case StageOutline:
			fn = ex.Outline
		case StageArticle:
			fn = ex.Article
		case StagePolish:
			if opts.SkipPolish {
				fn = nil
			} else {
				fn = ex.Polish
			}
		}
		job, err = o.Advance(ctx, job, stage, fn)
		if err != nil {
			return job, err
		}
	}
	return job, nil
}
