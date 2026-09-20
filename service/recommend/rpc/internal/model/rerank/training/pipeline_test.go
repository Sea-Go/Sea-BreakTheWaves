package training

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试训练流水线 Pipeline，覆盖：
//   - 样本收集（CollectSamples，mock SampleRepo）
//   - 全量训练（TrainFull，mock Trainer + ModelRegistry）
//   - 增量训练（TrainIncremental）
//   - MaybeTrain 时间间隔判断（全量/增量/跳过）
//   - A/B 分桶（AssignABucket 确定性）
//   - 标签映射（LabelFromEventType）
//   - 事件转样本（EventsToSamples）
// ============================================================================

// ---- mock 实现 ----

// mockSampleRepo 模拟样本仓库。
type mockSampleRepo struct {
	mu       sync.Mutex
	samples  []TrainingSample
	err      error
	called   int
	sinceArg time.Time
}

func (m *mockSampleRepo) CollectSamples(_ context.Context, since time.Time) ([]TrainingSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	m.sinceArg = since
	if m.err != nil {
		return nil, m.err
	}
	return m.samples, nil
}

// mockModelRegistry 模拟模型注册中心。
type mockModelRegistry struct {
	mu         sync.Mutex
	registered map[string][]registeredModel
	err        error
}

type registeredModel struct {
	model   []byte
	version string
}

func newMockModelRegistry() *mockModelRegistry {
	return &mockModelRegistry{registered: make(map[string][]registeredModel)}
}

func (m *mockModelRegistry) RegisterModel(_ context.Context, name string, model []byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.registered[name] = append(m.registered[name], registeredModel{model: model, version: version})
	return nil
}

func (m *mockModelRegistry) GetLatestModel(_ context.Context, name string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	models := m.registered[name]
	if len(models) == 0 {
		return nil, "", errors.New("model not found")
	}
	last := models[len(models)-1]
	return last.model, last.version, nil
}

// mockTrainer 模拟训练器。
type mockTrainer struct {
	name        string
	trainErr    error
	trainCalled int
	mu          sync.Mutex
	lastSamples []TrainingSample
}

func (m *mockTrainer) Train(samples []TrainingSample) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trainCalled++
	m.lastSamples = samples
	if m.trainErr != nil {
		return nil, m.trainErr
	}
	return []byte("mock_model_" + m.name), nil
}

func (m *mockTrainer) Name() string { return m.name }

// ---- 测试用例 ----

// TestLabelFromEventType 验证事件类型到标签的映射。
func TestLabelFromEventType(t *testing.T) {
	cases := []struct {
		eventType string
		want      float64
	}{
		{domain.EventClick, 1.0},
		{domain.EventLike, 1.0},
		{domain.EventFavorite, 1.0},
		{domain.EventReadComplete, 1.0},
		{domain.EventImpression, 0.0},
		{domain.EventDislike, -1.0},
		{"unknown_event", 0.0},
		{"", 0.0},
	}
	for _, c := range cases {
		got := LabelFromEventType(c.eventType)
		if got != c.want {
			t.Errorf("LabelFromEventType(%q) = %v, 期望 %v", c.eventType, got, c.want)
		}
	}
}

// TestEventsToSamples 验证 BehaviorEvent → TrainingSample 转换。
func TestEventsToSamples(t *testing.T) {
	events := []domain.BehaviorEvent{
		{UserID: "u1", ArticleID: "a1", EventType: domain.EventClick, Timestamp: time.Now()},
		{UserID: "u2", ArticleID: "a2", EventType: domain.EventImpression, Timestamp: time.Now()},
		{UserID: "u3", ArticleID: "a3", EventType: domain.EventDislike, Timestamp: time.Now()},
		{UserID: "", ArticleID: "a4", EventType: domain.EventClick}, // 跳过：UserID 为空
		{UserID: "u5", ArticleID: "", EventType: domain.EventClick}, // 跳过：ArticleID 为空
	}

	samples := EventsToSamples(events)
	if len(samples) != 3 {
		t.Fatalf("EventsToSamples 返回 %d 个样本, 期望 3", len(samples))
	}

	// 验证标签映射。
	if samples[0].Label != 1.0 {
		t.Errorf("样本 0 标签 = %v, 期望 1.0 (click)", samples[0].Label)
	}
	if samples[1].Label != 0.0 {
		t.Errorf("样本 1 标签 = %v, 期望 0.0 (impression)", samples[1].Label)
	}
	if samples[2].Label != -1.0 {
		t.Errorf("样本 2 标签 = %v, 期望 -1.0 (dislike)", samples[2].Label)
	}

	// 验证 UserID/ArticleID 传递。
	if samples[0].UserID != "u1" || samples[0].ArticleID != "a1" {
		t.Errorf("样本 0 UserID/ArticleID 不匹配: %+v", samples[0])
	}
}

// TestPipeline_CollectSamples 验证样本收集委托给 SampleRepo。
func TestPipeline_CollectSamples(t *testing.T) {
	expected := []TrainingSample{
		{UserID: "u1", ArticleID: "a1", Label: 1.0, Timestamp: time.Now()},
		{UserID: "u2", ArticleID: "a2", Label: 0.0, Timestamp: time.Now()},
	}
	repo := &mockSampleRepo{samples: expected}
	p := NewPipeline(repo, newMockModelRegistry())

	ctx := context.Background()
	since := time.Now().Add(-time.Hour)
	got, err := p.CollectSamples(ctx, since)
	if err != nil {
		t.Fatalf("CollectSamples 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("CollectSamples 返回 %d 个样本, 期望 2", len(got))
	}
	if repo.called != 1 {
		t.Errorf("SampleRepo.CollectSamples 调用次数 = %d, 期望 1", repo.called)
	}
	if !repo.sinceArg.Equal(since) {
		t.Errorf("since 参数 = %v, 期望 %v", repo.sinceArg, since)
	}
}

// TestPipeline_TrainFull 验证全量训练流程。
func TestPipeline_TrainFull(t *testing.T) {
	samples := []TrainingSample{
		{UserID: "u1", ArticleID: "a1", Label: 1.0, Timestamp: time.Now()},
		{UserID: "u2", ArticleID: "a2", Label: 0.0, Timestamp: time.Now()},
	}
	repo := &mockSampleRepo{samples: samples}
	registry := newMockModelRegistry()
	p := NewPipeline(repo, registry)

	trainer1 := &mockTrainer{name: "cross_encoder"}
	trainer2 := &mockTrainer{name: "two_tower"}
	p.AddTrainer(trainer1)
	p.AddTrainer(trainer2)

	ctx := context.Background()
	if err := p.TrainFull(ctx); err != nil {
		t.Fatalf("TrainFull 失败: %v", err)
	}

	// 验证训练器被调用。
	if trainer1.trainCalled != 1 {
		t.Errorf("trainer1 训练次数 = %d, 期望 1", trainer1.trainCalled)
	}
	if trainer2.trainCalled != 1 {
		t.Errorf("trainer2 训练次数 = %d, 期望 1", trainer2.trainCalled)
	}

	// 验证模型注册。
	if len(registry.registered["cross_encoder"]) != 1 {
		t.Errorf("cross_encoder 注册次数 = %d, 期望 1", len(registry.registered["cross_encoder"]))
	}
	if len(registry.registered["two_tower"]) != 1 {
		t.Errorf("two_tower 注册次数 = %d, 期望 1", len(registry.registered["two_tower"]))
	}

	// 验证版本号格式 v20260701_120000。
	reg := registry.registered["cross_encoder"][0]
	if len(reg.version) < 16 || reg.version[0] != 'v' {
		t.Errorf("版本号格式错误: %q", reg.version)
	}

	// 验证 lastFullRetrain 更新。
	if p.GetLastFullRetrain().IsZero() {
		t.Error("TrainFull 后 lastFullRetrain 未更新")
	}
	if p.GetLastIncremental().IsZero() {
		t.Error("TrainFull 后 lastIncremental 未更新")
	}
}

// TestPipeline_TrainFullNoTrainers 验证无训练器时报错。
func TestPipeline_TrainFullNoTrainers(t *testing.T) {
	repo := &mockSampleRepo{samples: []TrainingSample{{UserID: "u1", ArticleID: "a1", Label: 1.0}}}
	p := NewPipeline(repo, newMockModelRegistry())

	err := p.TrainFull(context.Background())
	if err == nil {
		t.Fatal("无训练器时 TrainFull 应返回错误")
	}
}

// TestPipeline_TrainFullEmptySamples 验证样本不足时报错。
func TestPipeline_TrainFullEmptySamples(t *testing.T) {
	repo := &mockSampleRepo{samples: nil}
	p := NewPipeline(repo, newMockModelRegistry())
	p.AddTrainer(&mockTrainer{name: "ce"})

	err := p.TrainFull(context.Background())
	if err == nil {
		t.Fatal("样本为空时 TrainFull 应返回错误")
	}
}

// TestPipeline_TrainIncremental 验证增量训练流程。
func TestPipeline_TrainIncremental(t *testing.T) {
	samples := []TrainingSample{
		{UserID: "u1", ArticleID: "a1", Label: 1.0, Timestamp: time.Now()},
	}
	repo := &mockSampleRepo{samples: samples}
	registry := newMockModelRegistry()
	p := NewPipeline(repo, registry)
	p.AddTrainer(&mockTrainer{name: "cross_encoder"})

	ctx := context.Background()
	if err := p.TrainIncremental(ctx); err != nil {
		t.Fatalf("TrainIncremental 失败: %v", err)
	}

	if p.GetLastIncremental().IsZero() {
		t.Error("TrainIncremental 后 lastIncremental 未更新")
	}
	if len(registry.registered["cross_encoder"]) != 1 {
		t.Errorf("增量训练后模型注册次数 = %d, 期望 1", len(registry.registered["cross_encoder"]))
	}
}

// TestPipeline_TrainIncrementalEmptySamples 验证增量为空时跳过训练不报错。
func TestPipeline_TrainIncrementalEmptySamples(t *testing.T) {
	repo := &mockSampleRepo{samples: nil}
	p := NewPipeline(repo, newMockModelRegistry())
	trainer := &mockTrainer{name: "ce"}
	p.AddTrainer(trainer)

	err := p.TrainIncremental(context.Background())
	if err != nil {
		t.Fatalf("增量为空时 TrainIncremental 不应报错, got: %v", err)
	}
	if trainer.trainCalled != 0 {
		t.Errorf("增量为空时训练器不应被调用, got: %d", trainer.trainCalled)
	}
}

// TestPipeline_TrainIncrementalAfterFull 验证全量训练后增量训练的 since 参数。
func TestPipeline_TrainIncrementalAfterFull(t *testing.T) {
	samples := []TrainingSample{
		{UserID: "u1", ArticleID: "a1", Label: 1.0, Timestamp: time.Now()},
	}
	repo := &mockSampleRepo{samples: samples}
	p := NewPipeline(repo, newMockModelRegistry())
	p.AddTrainer(&mockTrainer{name: "ce"})

	ctx := context.Background()
	// 先做一次全量训练。
	if err := p.TrainFull(ctx); err != nil {
		t.Fatalf("TrainFull 失败: %v", err)
	}
	fullTime := p.GetLastFullRetrain()

	// 再做增量训练。
	if err := p.TrainIncremental(ctx); err != nil {
		t.Fatalf("TrainIncremental 失败: %v", err)
	}

	// 增量训练的 since 应为全量训练时间（非零值）。
	if !repo.sinceArg.Equal(fullTime) {
		t.Errorf("增量训练 since = %v, 期望 %v (全量训练时间)", repo.sinceArg, fullTime)
	}
}

// TestPipeline_MaybeTrainSkip 验证 MaybeTrain 在间隔内跳过。
func TestPipeline_MaybeTrainSkip(t *testing.T) {
	samples := []TrainingSample{
		{UserID: "u1", ArticleID: "a1", Label: 1.0, Timestamp: time.Now()},
	}
	repo := &mockSampleRepo{samples: samples}
	p := NewPipeline(repo, newMockModelRegistry())
	p.AddTrainer(&mockTrainer{name: "ce"})

	ctx := context.Background()
	// 第一次触发全量训练。
	if err := p.MaybeTrain(ctx); err != nil {
		t.Fatalf("首次 MaybeTrain 失败: %v", err)
	}
	calledAfterFirst := repo.called

	// 立即再次触发，应跳过（未到间隔）。
	if err := p.MaybeTrain(ctx); err != nil {
		t.Fatalf("二次 MaybeTrain 失败: %v", err)
	}
	if repo.called != calledAfterFirst {
		t.Errorf("间隔内 MaybeTrain 不应触发训练, called = %d, 期望 %d", repo.called, calledAfterFirst)
	}
}

// TestPipeline_MaybeTrainIncremental 验证 MaybeTrain 触发增量训练。
func TestPipeline_MaybeTrainIncremental(t *testing.T) {
	samples := []TrainingSample{
		{UserID: "u1", ArticleID: "a1", Label: 1.0, Timestamp: time.Now()},
	}
	repo := &mockSampleRepo{samples: samples}
	p := NewPipeline(repo, newMockModelRegistry())
	p.AddTrainer(&mockTrainer{name: "ce"})

	// 设置短间隔便于测试。
	p.SetRetrainIntervals(24*time.Hour, 50*time.Millisecond)

	ctx := context.Background()
	// 第一次触发全量训练。
	if err := p.MaybeTrain(ctx); err != nil {
		t.Fatalf("首次 MaybeTrain 失败: %v", err)
	}
	calledAfterFirst := repo.called

	// 等待增量间隔到期。
	time.Sleep(60 * time.Millisecond)

	// 第二次触发增量训练。
	if err := p.MaybeTrain(ctx); err != nil {
		t.Fatalf("二次 MaybeTrain 失败: %v", err)
	}
	if repo.called <= calledAfterFirst {
		t.Errorf("增量间隔到期后 MaybeTrain 应触发训练, called = %d, 期望 > %d", repo.called, calledAfterFirst)
	}
}

// TestPipeline_AssignABucket 验证 A/B 分桶的确定性与分布。
func TestPipeline_AssignABucket(t *testing.T) {
	p := NewPipeline(&mockSampleRepo{}, newMockModelRegistry())

	// 验证确定性：同一 userID 始终返回同一桶。
	bucket1 := p.AssignABucket("user_123")
	if bucket1 != "self" && bucket1 != "external" {
		t.Errorf("分桶结果 %q 不合法", bucket1)
	}
	for i := 0; i < 10; i++ {
		bucket2 := p.AssignABucket("user_123")
		if bucket1 != bucket2 {
			t.Errorf("同一 userID 分桶不稳定: %q vs %q", bucket1, bucket2)
		}
	}

	// 验证分布：大量不同用户应大致 50/50 分布。
	selfCount := 0
	externalCount := 0
	for i := 0; i < 1000; i++ {
		// 使用数值型 userID 确保哈希分布均匀。
		userID := fmt.Sprintf("user_%d", i)
		switch p.AssignABucket(userID) {
		case "self":
			selfCount++
		case "external":
			externalCount++
		}
	}
	if selfCount == 0 || externalCount == 0 {
		t.Errorf("A/B 分桶分布异常: self=%d, external=%d", selfCount, externalCount)
	}
	// 验证大致均匀（允许 ±20% 偏差）。
	if selfCount < 300 || externalCount < 300 {
		t.Errorf("A/B 分桶分布不均匀: self=%d, external=%d", selfCount, externalCount)
	}
}

// TestPipeline_TrainerError 验证训练器报错时 TrainFull 返回错误。
func TestPipeline_TrainerError(t *testing.T) {
	repo := &mockSampleRepo{samples: []TrainingSample{{UserID: "u1", ArticleID: "a1", Label: 1.0}}}
	p := NewPipeline(repo, newMockModelRegistry())
	p.AddTrainer(&mockTrainer{name: "ce", trainErr: errors.New("training failed")})

	err := p.TrainFull(context.Background())
	if err == nil {
		t.Fatal("训练器报错时 TrainFull 应返回错误")
	}
}

// TestPipeline_SampleRepoError 验证样本仓库报错时传播错误。
func TestPipeline_SampleRepoError(t *testing.T) {
	repo := &mockSampleRepo{err: errors.New("repo unavailable")}
	p := NewPipeline(repo, newMockModelRegistry())
	p.AddTrainer(&mockTrainer{name: "ce"})

	err := p.TrainFull(context.Background())
	if err == nil {
		t.Fatal("样本仓库报错时 TrainFull 应返回错误")
	}
}

// TestPipeline_ModelVersionFormat 验证模型版本号格式。
func TestPipeline_ModelVersionFormat(t *testing.T) {
	t1 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	v := modelVersion(t1)
	want := "v20260701_120000"
	if v != want {
		t.Errorf("modelVersion() = %q, 期望 %q", v, want)
	}
}

// TestScheduler_StopIdempotent 验证 Scheduler.Stop 多次调用安全。
func TestScheduler_StopIdempotent(t *testing.T) {
	p := NewPipeline(&mockSampleRepo{}, newMockModelRegistry())
	s := NewScheduler(p)

	// 第一次 Stop 应正常。
	s.Stop()
	// 第二次 Stop 不应 panic。
	s.Stop()
}
