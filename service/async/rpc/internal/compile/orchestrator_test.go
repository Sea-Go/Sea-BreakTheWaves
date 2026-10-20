// ============================================================================
// orchestrator_test.go —— C4 编排状态机测试：全流程推进、幂等跳过、
// 失败不回退+重试、do_polish 跳过、前置校验、Run 断点续跑、JSON 落盘
// 往返、NewCompileJob 入口校验。
// ============================================================================

package compile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// countingFunc 包装一个 StageFunc，记录调用次数与每次输入（测试探针）。
type countingFunc struct {
	inner  StageFunc
	calls  int
	inputs []StageInput
}

func (c *countingFunc) Execute(ctx context.Context, in StageInput) (StageOutput, error) {
	c.calls++
	c.inputs = append(c.inputs, in)
	return c.inner.Execute(ctx, in)
}

// failFunc 固定失败的执行器（错误注入）。
type failFunc struct{ err error }

func (f failFunc) Execute(ctx context.Context, in StageInput) (StageOutput, error) {
	return StageOutput{}, f.err
}

// devSources 构造 n 个确定性资料源（摘要含可提取词元）。
func devSources(n int) []SourceRef {
	srcs := make([]SourceRef, 0, n)
	for i := 0; i < n; i++ {
		srcs = append(srcs, SourceRef{
			DocKey:     "doc-" + string(rune('a'+i)),
			Summary:    "检索系统与整篇编码是知识平台的基座",
			RevisionID: "rev-" + string(rune('A'+i)),
		})
	}
	return srcs
}

// newDevJob 建一个可走完全流程的初始任务。
func newDevJob(t *testing.T) CompileJob {
	t.Helper()
	job, err := NewCompileJob("job-1", "mod-1", devSources(3))
	if err != nil {
		t.Fatalf("NewCompileJob: %v", err)
	}
	return job
}

// runAll 用给定执行器集合跑完四阶段。
func runAll(t *testing.T, job CompileJob, ex Executor) (CompileJob, error) {
	t.Helper()
	return NewOrchestrator().Run(context.Background(), job, RunOptions{}, ex)
}

// TestNewCompileJob 校验任务构造的入口契约。
func TestNewCompileJob(t *testing.T) {
	job, err := NewCompileJob("job-1", "mod-1", devSources(2))
	if err != nil {
		t.Fatalf("合法输入不应报错: %v", err)
	}
	if job.Status != StatusPending {
		t.Fatalf("初始状态应为 pending，得到 %q", job.Status)
	}
	if len(job.Stages) != 0 {
		t.Fatalf("初始阶段表应为空，得到 %d 项", len(job.Stages))
	}
	if err := (func() error {
		_, err := NewCompileJob("", "mod-1", devSources(1))
		return err
	})(); !errors.Is(err, ErrEmptyJob) {
		t.Fatalf("空 job_id 应报 ErrEmptyJob，得到 %v", err)
	}
	if _, err := NewCompileJob("job-1", "", devSources(1)); !errors.Is(err, ErrEmptyJob) {
		t.Fatalf("空 module_id 应报 ErrEmptyJob，得到 %v", err)
	}
	if _, err := NewCompileJob("job-1", "mod-1", nil); !errors.Is(err, ErrEmptyJob) {
		t.Fatalf("空 sources 应报 ErrEmptyJob，得到 %v", err)
	}
	bad := []SourceRef{{DocKey: "", Summary: "x"}}
	if _, err := NewCompileJob("job-1", "mod-1", bad); !errors.Is(err, ErrEmptyJob) {
		t.Fatalf("空 doc_key 应报 ErrEmptyJob，得到 %v", err)
	}
}

// TestAdvanceFullFlow 校验四阶段顺序推进：curate→outline→article→polish
// 逐段推进后 done；阶段间接线（输入装配）与终稿形态正确。
func TestAdvanceFullFlow(t *testing.T) {
	job := newDevJob(t)
	orch := NewOrchestrator()

	curate := &countingFunc{inner: DevCurator{}}
	outline := &countingFunc{inner: DevOutliner{}}
	article := &countingFunc{inner: DevArticleWriter{}}
	polish := &countingFunc{inner: DevPolisher{}}

	var err error
	if job, err = orch.Advance(context.Background(), job, StageCurate, curate); err != nil {
		t.Fatalf("curate: %v", err)
	}
	if job.Status != StatusOutlining {
		t.Fatalf("curate 完成后应处于 outlining，得到 %q", job.Status)
	}
	if job.Stages[StageCurate].Completed.IsZero() {
		t.Fatal("curate 应已记账完成时间")
	}
	if len(curate.inputs) != 1 || len(curate.inputs[0].Sources) != 3 {
		t.Fatalf("curate 输入应为 job.Sources（3 个），得到 %+v", curate.inputs)
	}

	if job, err = orch.Advance(context.Background(), job, StageOutline, outline); err != nil {
		t.Fatalf("outline: %v", err)
	}
	if job.Status != StatusArticling {
		t.Fatalf("outline 完成后应处于 articling，得到 %q", job.Status)
	}
	// 接线：outline 输入=curate 输出的要点清单。
	wantPoints := job.Output(StageCurate).Points
	if got := outline.inputs[0].Points; !reflect.DeepEqual(got, wantPoints) {
		t.Fatalf("outline 输入应等于 curate 的要点，得到 %v 想 %v", got, wantPoints)
	}

	if job, err = orch.Advance(context.Background(), job, StageArticle, article); err != nil {
		t.Fatalf("article: %v", err)
	}
	if job.Status != StatusPolishing {
		t.Fatalf("article 完成后应处于 polishing，得到 %q", job.Status)
	}
	// 接线：article 输入=sources+points+outline。
	in := article.inputs[0]
	if len(in.Sources) != 3 || !reflect.DeepEqual(in.Points, wantPoints) || in.Outline == nil {
		t.Fatalf("article 输入装配不完整: %+v", in)
	}

	if job, err = orch.Advance(context.Background(), job, StagePolish, polish); err != nil {
		t.Fatalf("polish: %v", err)
	}
	if job.Status != StatusDone {
		t.Fatalf("polish 完成后应 done，得到 %q", job.Status)
	}
	// 终稿=润色稿（引用表携带、末尾声明在）。
	final := job.FinalArticle()
	if final == nil {
		t.Fatal("done 后应有终稿")
	}
	if !strings.Contains(final.FullText(), devPolishDeclaration) {
		t.Fatalf("终稿应含人工修订声明，得到 %q", final.FullText())
	}
	if len(final.Citations) != 3 || final.Citations[0].Index != 1 || final.Citations[2].DocKey != "doc-c" {
		t.Fatalf("终稿引用表应为 1..3 且 doc_key 来自 sources，得到 %+v", final.Citations)
	}
	if len(polish.inputs) != 1 || polish.inputs[0].Article == nil {
		t.Fatalf("polish 输入应为 article 全文，得到 %+v", polish.inputs)
	}
}

// TestAdvanceIdempotent 校验幂等：已完成阶段再次 Advance 直接跳过——
// 不调执行器、产物指针不变、状态不动。
func TestAdvanceIdempotent(t *testing.T) {
	job := newDevJob(t)
	orch := NewOrchestrator()
	curate := &countingFunc{inner: DevCurator{}}

	job, err := orch.Advance(context.Background(), job, StageCurate, curate)
	if err != nil {
		t.Fatalf("curate: %v", err)
	}
	before := job.Output(StageCurate)
	statusBefore := job.Status

	repeat := &countingFunc{inner: DevCurator{}}
	job2, err := orch.Advance(context.Background(), job, StageCurate, repeat)
	if err != nil {
		t.Fatalf("重复 curate 不应报错: %v", err)
	}
	if repeat.calls != 0 {
		t.Fatalf("已完成阶段不应再调执行器，调了 %d 次", repeat.calls)
	}
	if job2.Status != statusBefore {
		t.Fatalf("幂等跳过不应改状态，%q -> %q", statusBefore, job2.Status)
	}
	if job2.Output(StageCurate) != before {
		t.Fatal("幂等跳过应保留原产物（指针不变）")
	}
}

// TestAdvanceFailureRetry 校验失败不回退：阶段失败→failed+Err 记账、
// 原任务对象不被改写、已完成阶段保留；对同阶段重试成功后恢复推进。
func TestAdvanceFailureRetry(t *testing.T) {
	job := newDevJob(t)
	orch := NewOrchestrator()

	job, err := orch.Advance(context.Background(), job, StageCurate, DevCurator{})
	if err != nil {
		t.Fatalf("curate: %v", err)
	}

	boom := errors.New("大纲模型超时")
	failed, advErr := orch.Advance(context.Background(), job, StageOutline, failFunc{err: boom})
	if advErr == nil || !errors.Is(advErr, boom) {
		t.Fatalf("阶段失败应回传错误（可 errors.Is），得到 %v", advErr)
	}
	if failed.Status != StatusFailed {
		t.Fatalf("失败后状态应为 failed，得到 %q", failed.Status)
	}
	st := failed.Stages[StageOutline]
	if st == nil || st.Err == "" || !st.Completed.IsZero() {
		t.Fatalf("失败阶段应记账 Err 且未完成: %+v", st)
	}
	if !failed.Stages[StageCurate].done() {
		t.Fatal("失败不回退：curate 的完成记账应保留")
	}
	// 传入的原任务不被改写（值语义：调用方不接管返回值则 job 不变）。
	if job.Status != StatusOutlining || job.Stages[StageOutline] != nil {
		t.Fatalf("原任务不应被失败推进改写: status=%q outline=%v", job.Status, job.Stages[StageOutline])
	}

	// 重试同一阶段：成功后 Err 清空、状态恢复推进。
	retried, err := orch.Advance(context.Background(), failed, StageOutline, DevOutliner{})
	if err != nil {
		t.Fatalf("重试 outline: %v", err)
	}
	if retried.Status != StatusArticling {
		t.Fatalf("重试成功后应恢复到 articling，得到 %q", retried.Status)
	}
	st = retried.Stages[StageOutline]
	if st.Err != "" || st.Completed.IsZero() || st.Output == nil {
		t.Fatalf("重试成功后 Err 应清空并记完成: %+v", st)
	}
}

// TestSkipPolish 校验 do_polish 语义：nil 执行器跳过润色→Article 终稿
// 直接 done、polish 无记账；非 Polish 阶段传 nil 被拒。
func TestSkipPolish(t *testing.T) {
	job := newDevJob(t)
	orch := NewOrchestrator()

	// 非 Polish 阶段不可跳过（内容链前置）。
	if _, err := orch.Advance(context.Background(), job, StageCurate, nil); !errors.Is(err, ErrSkipNotAllowed) {
		t.Fatalf("curate 传 nil 应报 ErrSkipNotAllowed，得到 %v", err)
	}

	for _, st := range []Stage{StageCurate, StageOutline, StageArticle} {
		var fn StageFunc
		switch st {
		case StageCurate:
			fn = DevCurator{}
		case StageOutline:
			fn = DevOutliner{}
		case StageArticle:
			fn = DevArticleWriter{}
		}
		var err error
		if job, err = orch.Advance(context.Background(), job, st, fn); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	// do_polish=false：nil 执行器=显式跳过。
	job, err := orch.Advance(context.Background(), job, StagePolish, nil)
	if err != nil {
		t.Fatalf("跳过 polish 不应报错: %v", err)
	}
	if job.Status != StatusDone {
		t.Fatalf("跳过 polish 后应 done，得到 %q", job.Status)
	}
	if job.Stages[StagePolish] != nil {
		t.Fatalf("被跳过的阶段不应有记账: %+v", job.Stages[StagePolish])
	}
	// 终稿=Article 原稿（无人工修订声明）。
	final := job.FinalArticle()
	if final == nil || strings.Contains(final.FullText(), devPolishDeclaration) {
		t.Fatalf("跳过润色时终稿应为 Article 原稿，得到 %q", final.FullText())
	}
	if final != job.Output(StageArticle).Article {
		t.Fatal("终稿应直接复用 Article 产物")
	}
}

// TestRunSkipPolish 校验 Run 的 do_* 开关：SkipPolish=true 全链 done
// 且无 polish 记账。
func TestRunSkipPolish(t *testing.T) {
	job := newDevJob(t)
	done, err := NewOrchestrator().Run(context.Background(), job, RunOptions{SkipPolish: true}, DevExecutor())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if done.Status != StatusDone || done.Stages[StagePolish] != nil {
		t.Fatalf("SkipPolish 应直达 done 且无 polish 记账: status=%q polish=%v",
			done.Status, done.Stages[StagePolish])
	}
	if done.FinalArticle() != done.Output(StageArticle).Article {
		t.Fatal("SkipPolish 终稿应为 Article 原稿")
	}
}

// TestAdvanceOrderGuard 校验前置校验：跳阶段请求被拒（ErrStageOrder），
// 未知阶段被拒（ErrInvalidStage）。
func TestAdvanceOrderGuard(t *testing.T) {
	job := newDevJob(t)
	orch := NewOrchestrator()
	if _, err := orch.Advance(context.Background(), job, StageOutline, DevOutliner{}); !errors.Is(err, ErrStageOrder) {
		t.Fatalf("curate 未完成时 outline 应报 ErrStageOrder，得到 %v", err)
	}
	if _, err := orch.Advance(context.Background(), job, StagePolish, DevPolisher{}); !errors.Is(err, ErrStageOrder) {
		t.Fatalf("article 未完成时 polish 应报 ErrStageOrder，得到 %v", err)
	}
	if _, err := orch.Advance(context.Background(), job, Stage("bogus"), DevCurator{}); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("未知阶段应报 ErrInvalidStage，得到 %v", err)
	}
	if job.Status != StatusPending {
		t.Fatalf("被拒的推进不应改状态，得到 %q", job.Status)
	}
}

// TestRunResumeAfterFailure 校验断点续跑：Run 中途失败后重入 Run，
// 已完成阶段不重执行（幂等），从失败阶段续跑到 done。
func TestRunResumeAfterFailure(t *testing.T) {
	job := newDevJob(t)
	orch := NewOrchestrator()

	curate := &countingFunc{inner: DevCurator{}}
	ex := Executor{Curate: curate, Outline: failFunc{err: errors.New("大纲失败")}, Article: DevArticleWriter{}, Polish: DevPolisher{}}
	failed, err := orch.Run(context.Background(), job, RunOptions{}, ex)
	if err == nil {
		t.Fatal("outline 失败时 Run 应回传错误")
	}
	if failed.Status != StatusFailed || !failed.Stages[StageCurate].done() {
		t.Fatalf("失败现场应保留已完成阶段: %+v", failed.Stages)
	}

	// 重入 Run：curate 幂等跳过（仍只执行过一次），后续续跑完成。
	fixed := Executor{Curate: curate, Outline: DevOutliner{}, Article: DevArticleWriter{}, Polish: DevPolisher{}}
	done, err := orch.Run(context.Background(), failed, RunOptions{}, fixed)
	if err != nil {
		t.Fatalf("续跑: %v", err)
	}
	if done.Status != StatusDone {
		t.Fatalf("续跑后应 done，得到 %q", done.Status)
	}
	if curate.calls != 1 {
		t.Fatalf("curate 全程应只执行一次（幂等续跑），执行了 %d 次", curate.calls)
	}
	if done.FinalArticle() == nil {
		t.Fatal("续跑后应有终稿")
	}
}

// TestCompileJobJSONRoundTrip 校验任务（含各阶段产物）可 JSON 落盘并
// 无损恢复——C4"产物落盘、断点重跑"的数据前提。
func TestCompileJobJSONRoundTrip(t *testing.T) {
	job := newDevJob(t)
	done, err := runAll(t, job, DevExecutor())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	blob, err := json.Marshal(done)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var restored CompileJob
	if err := json.Unmarshal(blob, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if restored.Status != StatusDone || restored.JobID != done.JobID || restored.ModuleID != done.ModuleID {
		t.Fatalf("基础字段往返不一致: %+v", restored)
	}
	if len(restored.Sources) != len(done.Sources) {
		t.Fatalf("sources 往返不一致")
	}
	for _, st := range Stages() {
		if restored.Stages[st] == nil || !restored.Stages[st].done() {
			t.Fatalf("阶段 %s 记账往返缺失", st)
		}
	}
	if !reflect.DeepEqual(restored.FinalArticle(), done.FinalArticle()) {
		t.Fatalf("终稿往返不一致:\n得到 %v\n想要 %v", restored.FinalArticle(), done.FinalArticle())
	}
	// 恢复后的任务重入 Run 幂等直达 done。
	again, err := NewOrchestrator().Run(context.Background(), restored, RunOptions{}, DevExecutor())
	if err != nil || again.Status != StatusDone {
		t.Fatalf("恢复任务重入 Run 应幂等 done: err=%v status=%q", err, again.Status)
	}
}
