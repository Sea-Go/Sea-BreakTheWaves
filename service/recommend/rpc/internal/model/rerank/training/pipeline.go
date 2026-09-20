package training

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现训练流水线（Task 8.5），覆盖自研 rerank 模型的全量/增量训练。
//
// 核心能力：
//   - 从 BehaviorEvent 收集训练样本（click=1, impression=0, dislike=-1）
//   - 全量训练（每日）：全量样本 → Trainer.Train → ModelRegistry.RegisterModel
//   - 增量训练（每小时）：近一小时样本 → Trainer.Train → ModelRegistry.RegisterModel
//   - MaybeTrain：检查时间间隔自动触发全量或增量训练
//   - 模型版本化：时间戳格式 v20260701_120000
//   - A/B 分桶：基于 userID hash 分配 "self"/"external" 流量
//
// 二开扩展点：
//   - 实现 SampleRepo interface 自定义样本收集（如接入 Kafka/离线数仓）
//   - 实现 ModelRegistry interface 自定义模型存储（如接入 MinIO/OSS）
//   - 实现 Trainer interface 接入不同训练框架（PyTorch/XGBoost/ONNX Runtime）
//   - 调整 fullRetrainInterval/incrementalInterval 改变训练节奏
//   - 替换 AssignABucket 哈希算法或分桶比例
// ============================================================================

// 默认训练间隔。
const (
	// defaultFullRetrainInterval 默认全量训练间隔（每日）。
	defaultFullRetrainInterval = 24 * time.Hour
	// defaultIncrementalInterval 默认增量训练间隔（每小时）。
	defaultIncrementalInterval = time.Hour
	// minSamplesForTrain 训练所需最小样本数，低于此值跳过训练。
	minSamplesForTrain = 1
)

// TrainingSample 训练样本。
//
// 从 BehaviorEvent 转换而来，含用户/文章/特征/标签/时间戳。
// Label 语义：click=1（正样本），impression=0（负样本），dislike=-1（强负样本）。
type TrainingSample struct {
	// UserID 用户 ID。
	UserID string
	// ArticleID 文章 ID。
	ArticleID string
	// Features 特征 map（供训练器消费，如 user_embedding/article_embedding/cf_score 等）。
	Features map[string]float64
	// Label 标签（click=1, impression=0, dislike=-1）。
	Label float64
	// Timestamp 样本时间戳（取自 BehaviorEvent.Timestamp）。
	Timestamp time.Time
}

// SampleRepo 样本仓库 interface，从 BehaviorEvent 收集训练样本。
//
// 二开扩展点：实现该 interface 接入不同数据源（Kafka/PostgreSQL/离线数仓）。
// 实现可调用 EventsToSamples 辅助函数完成 BehaviorEvent → TrainingSample 转换。
type SampleRepo interface {
	// CollectSamples 收集 since 之后的训练样本。
	// since 为零值时表示全量收集。
	CollectSamples(ctx context.Context, since time.Time) ([]TrainingSample, error)
}

// ModelRegistry 模型注册中心 interface，管理训练产出的模型版本。
//
// 二开扩展点：实现该 interface 接入不同模型存储（MinIO/OSS/本地文件系统）。
type ModelRegistry interface {
	// RegisterModel 注册模型，name 为模型名，model 为模型字节，version 为版本号。
	RegisterModel(ctx context.Context, name string, model []byte, version string) error
	// GetLatestModel 获取指定模型的最新版本，返回模型字节、版本号与 error。
	GetLatestModel(ctx context.Context, name string) ([]byte, string, error)
}

// Trainer 训练器抽象 interface。
//
// 二开扩展点：为 CrossEncoder/TwoTower/LambdaMART 分别实现该 interface，
// 接入不同训练框架（PyTorch/XGBoost/ONNX Runtime）。
// Pipeline 通过 AddTrainer 注册多个训练器，全量/增量训练时逐一调用。
type Trainer interface {
	// Train 训练模型，返回模型字节（如 ONNX/SavedModel/JSON 权重）。
	Train(samples []TrainingSample) ([]byte, error)
	// Name 返回训练器名称（如 "cross_encoder"/"two_tower"/"lambda_mart"）。
	Name() string
}

// Pipeline 训练流水线，编排样本收集、模型训练与注册。
//
// 工作流程：
//  1. CollectSamples → 从 SampleRepo 获取训练样本
//  2. TrainFull/TrainIncremental → 调用各 Trainer 训练 → 注册到 ModelRegistry
//  3. MaybeTrain → 检查时间间隔自动触发全量或增量训练
//
// 线程安全，支持并发调用 MaybeTrain。
type Pipeline struct {
	// sampleRepo 样本仓库。
	sampleRepo SampleRepo
	// modelRegistry 模型注册中心。
	modelRegistry ModelRegistry
	// trainers 训练器列表（CrossEncoder/TwoTower/LambdaMART 等）。
	trainers []Trainer

	// fullRetrainInterval 全量训练间隔（默认每日）。
	fullRetrainInterval time.Duration
	// incrementalInterval 增量训练间隔（默认每小时）。
	incrementalInterval time.Duration

	// mu 保护以下字段的并发访问。
	mu sync.RWMutex
	// lastFullRetrain 上次全量训练时间。
	lastFullRetrain time.Time
	// lastIncremental 上次增量训练时间。
	lastIncremental time.Time
}

// NewPipeline 创建训练流水线，默认每日全量 / 每小时增量。
//
// sr 样本仓库；mr 模型注册中心。
// 二开：通过 SetRetrainIntervals 调整训练间隔，通过 AddTrainer 注册训练器。
func NewPipeline(sr SampleRepo, mr ModelRegistry) *Pipeline {
	return &Pipeline{
		sampleRepo:          sr,
		modelRegistry:       mr,
		fullRetrainInterval: defaultFullRetrainInterval,
		incrementalInterval: defaultIncrementalInterval,
	}
}

// AddTrainer 添加训练器。
//
// 二开：在装配阶段为 CrossEncoder/TwoTower/LambdaMART 分别注册 Trainer 实现。
func (p *Pipeline) AddTrainer(t Trainer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trainers = append(p.trainers, t)
}

// SetRetrainIntervals 调整全量/增量训练间隔（二开点）。
//
// full 全量训练间隔；incremental 增量训练间隔。
func (p *Pipeline) SetRetrainIntervals(full, incremental time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fullRetrainInterval = full
	p.incrementalInterval = incremental
}

// CollectSamples 收集训练样本，委托给 SampleRepo。
//
// since 为零值时全量收集；否则收集 since 之后的样本。
// 样本来源为 BehaviorEvent，标签映射：click=1, impression=0, dislike=-1。
func (p *Pipeline) CollectSamples(ctx context.Context, since time.Time) ([]TrainingSample, error) {
	return p.sampleRepo.CollectSamples(ctx, since)
}

// TrainFull 全量训练。
//
// 流程：
//  1. 从 SampleRepo 全量收集样本（since=零值）
//  2. 逐一调用 Trainer.Train 训练模型
//  3. 将模型字节注册到 ModelRegistry（版本号用时间戳 v20260701_120000）
//  4. 更新 lastFullRetrain 与 lastIncremental 时间戳
func (p *Pipeline) TrainFull(ctx context.Context) error {
	samples, err := p.sampleRepo.CollectSamples(ctx, time.Time{})
	if err != nil {
		return fmt.Errorf("全量训练收集样本失败: %w", err)
	}
	if len(samples) < minSamplesForTrain {
		return fmt.Errorf("全量训练样本不足: %d < %d", len(samples), minSamplesForTrain)
	}

	trainers := p.getTrainers()
	if len(trainers) == 0 {
		return fmt.Errorf("无已注册训练器")
	}

	version := modelVersion(time.Now())
	for _, t := range trainers {
		modelBytes, err := t.Train(samples)
		if err != nil {
			return fmt.Errorf("训练器 %s 训练失败: %w", t.Name(), err)
		}
		if err := p.modelRegistry.RegisterModel(ctx, t.Name(), modelBytes, version); err != nil {
			return fmt.Errorf("注册模型 %s 失败: %w", t.Name(), err)
		}
	}

	now := time.Now()
	p.mu.Lock()
	p.lastFullRetrain = now
	p.lastIncremental = now
	p.mu.Unlock()
	return nil
}

// TrainIncremental 增量训练。
//
// 流程：
//  1. 从 SampleRepo 收集上次增量训练之后的样本
//  2. 逐一调用 Trainer.Train 训练模型
//  3. 将模型字节注册到 ModelRegistry
//  4. 更新 lastIncremental 时间戳
//
// 若样本为空则跳过训练，仅更新时间戳。
func (p *Pipeline) TrainIncremental(ctx context.Context) error {
	p.mu.RLock()
	lastIncr := p.lastIncremental
	p.mu.RUnlock()

	since := lastIncr
	if since.IsZero() {
		// 首次增量训练，回溯一个增量间隔。
		since = time.Now().Add(-p.incrementalInterval)
	}

	samples, err := p.sampleRepo.CollectSamples(ctx, since)
	if err != nil {
		return fmt.Errorf("增量训练收集样本失败: %w", err)
	}

	// 样本为空时仅更新时间戳，不报错。
	if len(samples) == 0 {
		p.mu.Lock()
		p.lastIncremental = time.Now()
		p.mu.Unlock()
		return nil
	}

	trainers := p.getTrainers()
	if len(trainers) == 0 {
		return fmt.Errorf("无已注册训练器")
	}

	version := modelVersion(time.Now())
	for _, t := range trainers {
		modelBytes, err := t.Train(samples)
		if err != nil {
			return fmt.Errorf("训练器 %s 增量训练失败: %w", t.Name(), err)
		}
		if err := p.modelRegistry.RegisterModel(ctx, t.Name(), modelBytes, version); err != nil {
			return fmt.Errorf("注册模型 %s 失败: %w", t.Name(), err)
		}
	}

	p.mu.Lock()
	p.lastIncremental = time.Now()
	p.mu.Unlock()
	return nil
}

// MaybeTrain 检查时间间隔，自动触发全量或增量训练。
//
//   - 距上次全量训练 ≥ fullRetrainInterval → 全量训练
//   - 否则距上次增量训练 ≥ incrementalInterval → 增量训练
//   - 均未到间隔 → 跳过（返回 nil）
func (p *Pipeline) MaybeTrain(ctx context.Context) error {
	now := time.Now()
	p.mu.RLock()
	fullInterval := p.fullRetrainInterval
	incrInterval := p.incrementalInterval
	lastFull := p.lastFullRetrain
	lastIncr := p.lastIncremental
	p.mu.RUnlock()

	// 全量训练判断。
	if now.Sub(lastFull) >= fullInterval {
		return p.TrainFull(ctx)
	}

	// 增量训练判断。
	if now.Sub(lastIncr) >= incrInterval {
		return p.TrainIncremental(ctx)
	}

	return nil
}

// AssignABucket A/B 分桶，基于 userID hash 分配 "self"/"external" 流量。
//
// 使用 FNV-1a 哈希（stdlib）对 userID 做确定性分桶，保证同一用户始终落入同一桶。
// 二开扩展点：
//   - 替换哈希算法（如 murmur3）
//   - 调整分桶比例（如 70% self / 30% external）
//   - 按用户分层分流（如新用户全走 self）
func (p *Pipeline) AssignABucket(userID string) string {
	h := fnv.New32a()
	h.Write([]byte(userID))
	if h.Sum32()%2 == 0 {
		return "self"
	}
	return "external"
}

// GetLastFullRetrain 返回上次全量训练时间（线程安全，供监控/调试使用）。
func (p *Pipeline) GetLastFullRetrain() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastFullRetrain
}

// GetLastIncremental 返回上次增量训练时间（线程安全，供监控/调试使用）。
func (p *Pipeline) GetLastIncremental() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastIncremental
}

// getTrainers 获取训练器列表的快照（线程安全）。
func (p *Pipeline) getTrainers() []Trainer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Trainer, len(p.trainers))
	copy(out, p.trainers)
	return out
}

// modelVersion 生成模型版本号，格式 v20260701_120000（时间戳）。
func modelVersion(t time.Time) string {
	return fmt.Sprintf("v%s", t.Format("20060102_150405"))
}

// LabelFromEventType 根据行为事件类型生成训练标签。
//
// 标签语义：
//   - click/like/favorite/read_complete → 1.0（正样本）
//   - impression → 0.0（负样本，未点击）
//   - dislike → -1.0（强负样本，不感兴趣）
//   - 其他 → 0.0
//
// 二开扩展点：可扩展更多事件类型的标签映射（如 share=1.5 加权正样本）。
func LabelFromEventType(eventType string) float64 {
	switch eventType {
	case domain.EventClick, domain.EventLike,
		domain.EventFavorite, domain.EventReadComplete:
		return 1.0
	case domain.EventImpression:
		return 0.0
	case domain.EventDislike:
		return -1.0
	default:
		return 0.0
	}
}

// EventsToSamples 将 BehaviorEvent 列表转换为 TrainingSample 列表。
//
// 标签映射由 LabelFromEventType 决定。跳过 UserID 或 ArticleID 为空的事件。
// SampleRepo 实现可调用此辅助函数完成 BehaviorEvent → TrainingSample 转换。
func EventsToSamples(events []domain.BehaviorEvent) []TrainingSample {
	samples := make([]TrainingSample, 0, len(events))
	for _, e := range events {
		if e.UserID == "" || e.ArticleID == "" {
			continue
		}
		samples = append(samples, TrainingSample{
			UserID:    e.UserID,
			ArticleID: e.ArticleID,
			Label:     LabelFromEventType(e.EventType),
			Timestamp: e.Timestamp,
		})
	}
	return samples
}
