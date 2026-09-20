package rerank

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现在线 inference 服务（Task 8.6），提供自研/外部 rerank 的统一入口。
//
// 核心能力：
//   - 自研 rerank：CrossEncoder + TwoTower + LambdaMART 协同精排
//   - 外部 rerank：通过 ExternalReranker interface 调用 DashScope 等外部服务
//   - 降级机制：self 失败率超阈值时自动回退 external（circuit breaker）
//   - 模型热加载：HotReloadModel 无停机切换模型版本
//   - 健康检查：HealthCheck 探测模型可用性
//   - A/B 分桶：通过 RerankRequest.Model 选择 self/external
//
// 二开扩展点：
//   - 可替换模型：实现 CrossEncoderModel/TwoTowerModel/LambdaMARTModel 接口
//   - 可替换降级策略：调整 degradeThreshold/degradeCooldown 或重写 shouldDegrade
//   - 可替换外部 rerank：实现 ExternalReranker interface（如 Cohere/Jina）
//   - gRPC 对接：TODO 待 protoc 生成代码后，实现 gRPC service handler
// ============================================================================

// 降级相关默认值。
const (
	// defaultDegradeThreshold 默认降级阈值：self 失败率超过 30% 时降级。
	defaultDegradeThreshold = 0.3
	// defaultDegradeCooldown 默认降级冷却时间：降级后 30 秒内直接走 external。
	defaultDegradeCooldown = 30 * time.Second
	// minRequestsForDegrade 触发降级判断的最小请求数（避免冷启动误判）。
	minRequestsForDegrade = 10
)

// CrossEncoderModel Cross-encoder 模型抽象接口。
//
// 由于 cross_encoder.go 由并行任务（Task 8.1-8.4）创建，此处用 interface 抽象。
// 二开点：当 cross_encoder.go 创建具体 CrossEncoder 类型后，应实现该接口，
// 并可通过 type assertion 或 adapter 桥接。
//
// 接口方法：
//   - Score：对 query 与候选列表逐一交叉编码打分，返回与 candidates 等长的分数切片
type CrossEncoderModel interface {
	Score(ctx context.Context, query string, candidates []domain.Candidate) ([]float64, error)
}

// TwoTowerModel 双塔模型抽象接口。
//
// 二开点：当 two_tower.go 创建具体 TwoTower 类型后，应实现该接口。
//
// 接口方法：
//   - Score：对用户与候选文章双塔打分，返回与 candidates 等长的分数切片
type TwoTowerModel interface {
	Score(ctx context.Context, userID string, candidates []domain.Candidate) ([]float64, error)
}

// LambdaMARTModel LambdaMART 排序模型抽象接口。
//
// 二开点：当 lambda_mart.go 创建具体 LambdaMART 类型后，应实现该接口。
//
// 接口方法：
//   - Rank：listwise 排序候选，返回排序后的候选列表
type LambdaMARTModel interface {
	Rank(ctx context.Context, candidates []domain.Candidate) ([]domain.Candidate, error)
}

// ExternalReranker 外部 rerank 抽象（如 DashScope gte-rerank）。
// 定义于 abtest.go（与 ABTest 共享），此处不重复声明。
// Server 通过该 interface 调用外部 rerank 服务用于降级。
//
// 二开扩展点：实现该 interface 接入不同外部 rerank 服务（Cohere/Jina/自研 API）。

// Server 在线 inference 服务，统一入口 for 自研/外部 rerank。
//
// 工作流程：
//  1. Rerank：根据 req.Model 选择 self/external 路径
//     - self：CrossEncoder 打分 → TwoTower 打分 → 融合 → LambdaMART 终排
//     - external：直接调用 ExternalReranker
//     - 降级：self 失败率超阈值时自动回退 external
//  2. HotReloadModel：无停机切换模型（写锁保护，原子替换）
//  3. HealthCheck：探测模型可用性
//
// 线程安全，支持并发 Rerank 与 HotReloadModel。
type Server struct {
	// crossEncoder Cross-encoder 模型（可为 nil，表示未启用）。
	crossEncoder CrossEncoderModel
	// twoTower 双塔模型（可为 nil）。
	twoTower TwoTowerModel
	// ltr LambdaMART 终排模型（可为 nil）。
	ltr LambdaMARTModel
	// externalReranker 外部 rerank 服务（降级时使用）。
	externalReranker ExternalReranker

	// degradeThreshold self 失败率降级阈值（0-1）。
	degradeThreshold float64
	// degradeCooldown 降级冷却时间。
	degradeCooldown time.Duration

	// mu 保护以下字段的并发访问（主要用于 HotReloadModel 原子切换）。
	mu sync.RWMutex
	// currentModel 当前模型版本标识。
	currentModel string

	// 降级监控（circuit breaker）。
	// 使用原子操作避免与 Rerank 主路径锁竞争。
	totalRequests  int64 // self 请求总数
	failedRequests int64 // self 请求失败数
	// degradeUntil 降级截止时间，非零值表示处于降级状态。
	// 用 atomic int64 存储 UnixNano 时间戳，0 表示未降级。
	degradeUntilNano int64
}

// NewServer 创建在线 inference 服务。
//
// ce Cross-encoder 模型；tt 双塔模型；ltr LambdaMART 模型；
// external 外部 rerank 服务（用于降级）。
// 任一参数可为 nil，但至少需配置一个 self 模型或 external。
//
// 二开：通过 SetDegradeThreshold/SetDegradeCooldown 调整降级策略。
func NewServer(ce CrossEncoderModel, tt TwoTowerModel, ltr LambdaMARTModel, external ExternalReranker) *Server {
	return &Server{
		crossEncoder:     ce,
		twoTower:         tt,
		ltr:              ltr,
		externalReranker: external,
		degradeThreshold: defaultDegradeThreshold,
		degradeCooldown:  defaultDegradeCooldown,
		currentModel:     ModelSelf,
	}
}

// Rerank 在线推理，实现 domain.Reranker.Rerank 的核心逻辑。
//
// 路径选择（根据 req.Model）：
//   - "external"：直接走外部 rerank
//   - "self" 或空值：走自研 rerank（含降级逻辑）
//   - "llm" 或其他：TODO 待实现，当前回退到 self
//
// 降级逻辑（仅 self 路径）：
//  1. 若当前处于降级状态（degradeUntil 未过期），直接走 external
//  2. self 推理失败时，本次请求回退 external
//  3. self 失败率超阈值时，进入降级冷却期
//
// 返回 RerankResult，ModelUsed 标识实际使用的模型（self/external）。
func (s *Server) Rerank(ctx context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	model := req.Model
	if model == "" {
		model = ModelSelf
	}

	// external 路径：直接走外部 rerank。
	if model == ModelExternal {
		return s.rerankExternal(ctx, req)
	}

	// self 路径（含 "self"/"llm"/其他）。
	// 检查是否处于降级状态。
	if s.isDegraded() {
		// 降级中：直接走 external（若 external 不可用则返回错误）。
		return s.rerankExternal(ctx, req)
	}

	// 尝试 self 推理。
	result, err := s.rerankSelf(ctx, req)
	if err != nil {
		// self 失败：记录失败并尝试降级到 external。
		s.recordSelfFailure()
		if s.externalReranker != nil {
			// 本次请求降级到 external。
			extResult, extErr := s.rerankExternal(ctx, req)
			if extErr == nil {
				return extResult, nil
			}
			// external 也失败，返回原始 self 错误。
		}
		return domain.RerankResult{}, err
	}

	// self 成功：记录成功。
	s.recordSelfSuccess()
	return result, nil
}

// rerankSelf 自研 rerank：CrossEncoder + TwoTower + LambdaMART 协同精排。
//
// 流程：
//  1. CrossEncoder 对 query-doc 对打分（搜索场景）
//  2. TwoTower 对 user-article 打分（推荐场景）
//  3. 加权融合分数（ce 60% + tt 40%，二开可调）
//  4. LambdaMART listwise 终排（若启用）
//  5. TopK 截断
func (s *Server) rerankSelf(ctx context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	s.mu.RLock()
	ce := s.crossEncoder
	tt := s.twoTower
	ltr := s.ltr
	s.mu.RUnlock()

	// 至少需要一个 self 模型。
	if ce == nil && tt == nil && ltr == nil {
		return domain.RerankResult{}, fmt.Errorf("self rerank: 无可用自研模型")
	}

	if len(req.Candidates) == 0 {
		return domain.RerankResult{Candidates: nil, ModelUsed: ModelSelf}, nil
	}

	// 复制候选列表，不修改入参。
	cands := make([]domain.Candidate, len(req.Candidates))
	copy(cands, req.Candidates)

	// 1. CrossEncoder 打分（需有 query）。
	if ce != nil && req.Query != "" {
		scores, err := ce.Score(ctx, req.Query, cands)
		if err != nil {
			return domain.RerankResult{}, fmt.Errorf("cross_encoder 打分失败: %w", err)
		}
		if len(scores) == len(cands) {
			for i := range cands {
				if cands[i].Scores == nil {
					cands[i].Scores = make(map[string]float64)
				}
				cands[i].Scores["cross_encoder_score"] = scores[i]
			}
		}
	}

	// 2. TwoTower 打分（需有 userID）。
	if tt != nil && req.UserKey.UserID != "" {
		scores, err := tt.Score(ctx, req.UserKey.UserID, cands)
		if err != nil {
			return domain.RerankResult{}, fmt.Errorf("two_tower 打分失败: %w", err)
		}
		if len(scores) == len(cands) {
			for i := range cands {
				if cands[i].Scores == nil {
					cands[i].Scores = make(map[string]float64)
				}
				cands[i].Scores["two_tower_score"] = scores[i]
			}
		}
	}

	// 3. 融合分数（加权，二开点：可通过 RecommendConfig.RerankWeights 调整权重）。
	for i := range cands {
		ceScore := cands[i].Scores["cross_encoder_score"]
		ttScore := cands[i].Scores["two_tower_score"]
		cands[i].Score = 0.6*ceScore + 0.4*ttScore
	}

	// 4. LambdaMART listwise 终排（若启用）；否则按融合分数降序。
	if ltr != nil {
		ranked, err := ltr.Rank(ctx, cands)
		if err != nil {
			return domain.RerankResult{}, fmt.Errorf("lambda_mart 排序失败: %w", err)
		}
		cands = ranked
	} else {
		sort.SliceStable(cands, func(i, j int) bool {
			return cands[i].Score > cands[j].Score
		})
	}

	// 5. TopK 截断。
	topK := req.TopK
	if topK <= 0 || topK > len(cands) {
		topK = len(cands)
	}
	cands = cands[:topK]

	return domain.RerankResult{Candidates: cands, ModelUsed: ModelSelf}, nil
}

// rerankExternal 外部 rerank，委托给 ExternalReranker。
func (s *Server) rerankExternal(ctx context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	s.mu.RLock()
	external := s.externalReranker
	s.mu.RUnlock()

	if external == nil {
		return domain.RerankResult{}, fmt.Errorf("external rerank: 外部 reranker 未配置")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = len(req.Candidates)
	}

	cands, err := external.Rerank(ctx, req.Query, req.Candidates, topK)
	if err != nil {
		return domain.RerankResult{}, fmt.Errorf("external rerank 失败: %w", err)
	}

	return domain.RerankResult{Candidates: cands, ModelUsed: ModelExternal}, nil
}

// HotReloadModel 模型热加载（无停机切换）。
//
// 通过写锁保护，原子替换当前模型版本标识。
// 二开扩展点：
//   - 实际模型文件加载逻辑（如从 modelPath 加载 ONNX/BERT 权重）
//   - 模型验证（如 sanity check 推理）
//   - 灰度切换（如先切 10% 流量到新模型）
//
// TODO（二开点）：
//   - 根据 modelPath 后缀选择加载器（.onnx → ONNX Runtime, .pt → PyTorch, .json → XGBoost）
//   - 加载新模型后替换 CrossEncoderModel/TwoTowerModel/LambdaMARTModel 实例
//   - 返回旧模型供回滚
func (s *Server) HotReloadModel(ctx context.Context, modelPath, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// TODO（二开点）：
	// 1. 从 modelPath 加载模型文件（ONNX/SavedModel/XGBoost JSON）
	// 2. 验证模型可用性（sanity check 推理）
	// 3. 原子替换对应模型实例（crossEncoder/twoTower/ltr）
	// 4. 可选：保留旧模型引用供回滚

	// 当前仅更新版本标识。
	s.currentModel = version
	return nil
}

// HealthCheck 健康检查，探测模型可用性。
//
// 检查项：
//   - 至少有一个可用模型（self 或 external）
//   - 若处于降级状态，检查 external 是否可用
//
// 二开扩展点：可扩展为实际推理探测（如发送 1 条测试请求验证模型响应）。
func (s *Server) HealthCheck(ctx context.Context) error {
	s.mu.RLock()
	ce := s.crossEncoder
	tt := s.twoTower
	ltr := s.ltr
	external := s.externalReranker
	s.mu.RUnlock()

	hasSelf := ce != nil || tt != nil || ltr != nil
	hasExternal := external != nil

	if !hasSelf && !hasExternal {
		return fmt.Errorf("健康检查失败: 无可用模型（self 和 external 均未配置）")
	}

	// 若处于降级状态，必须有 external 兜底。
	if s.isDegraded() && !hasExternal {
		return fmt.Errorf("健康检查失败: 处于降级状态但 external 不可用")
	}

	return nil
}

// GetCurrentModel 返回当前模型版本标识（线程安全）。
func (s *Server) GetCurrentModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentModel
}

// SetDegradeThreshold 设置降级阈值（二开点）。
//
// threshold 取值范围 0-1，表示 self 失败率超过该值时触发降级。
func (s *Server) SetDegradeThreshold(threshold float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.degradeThreshold = threshold
}

// SetDegradeCooldown 设置降级冷却时间（二开点）。
func (s *Server) SetDegradeCooldown(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.degradeCooldown = d
}

// SetModels 原子替换模型实例（用于热加载，二开点）。
//
// ce/tt/ltr/external 为 nil 时保持原模型不变。
func (s *Server) SetModels(ce CrossEncoderModel, tt TwoTowerModel, ltr LambdaMARTModel, external ExternalReranker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ce != nil {
		s.crossEncoder = ce
	}
	if tt != nil {
		s.twoTower = tt
	}
	if ltr != nil {
		s.ltr = ltr
	}
	if external != nil {
		s.externalReranker = external
	}
}

// IsDegraded 返回是否处于降级状态（供监控/调试使用）。
func (s *Server) IsDegraded() bool {
	return s.isDegraded()
}

// ---- 降级监控（circuit breaker）实现 ----

// recordSelfSuccess 记录 self 请求成功。
func (s *Server) recordSelfSuccess() {
	atomic.AddInt64(&s.totalRequests, 1)
}

// recordSelfFailure 记录 self 请求失败，并在失败率超阈值时触发降级。
func (s *Server) recordSelfFailure() {
	total := atomic.AddInt64(&s.totalRequests, 1)
	failed := atomic.AddInt64(&s.failedRequests, 1)

	// 未达到最小请求数时不触发降级（避免冷启动误判）。
	if total < minRequestsForDegrade {
		return
	}

	s.mu.RLock()
	threshold := s.degradeThreshold
	cooldown := s.degradeCooldown
	s.mu.RUnlock()

	// 计算失败率。
	rate := float64(failed) / float64(total)
	if rate > threshold {
		// 触发降级：设置降级截止时间。
		degradeUntil := time.Now().Add(cooldown)
		atomic.StoreInt64(&s.degradeUntilNano, degradeUntil.UnixNano())
	}
}

// isDegraded 检查是否处于降级状态。
func (s *Server) isDegraded() bool {
	degradeUntilNano := atomic.LoadInt64(&s.degradeUntilNano)
	if degradeUntilNano == 0 {
		return false
	}
	degradeUntil := time.Unix(0, degradeUntilNano)
	if time.Now().Before(degradeUntil) {
		return true
	}
	// 降级已过期，重置状态。
	atomic.CompareAndSwapInt64(&s.degradeUntilNano, degradeUntilNano, 0)
	// 重置计数器，开始新一轮统计。
	atomic.StoreInt64(&s.totalRequests, 0)
	atomic.StoreInt64(&s.failedRequests, 0)
	return false
}

// 编译期断言：*Server 实现 domain.Reranker interface。
var _ domain.Reranker = (*Server)(nil)

// Name 返回重排器名称，实现 domain.Reranker.Name。
func (s *Server) Name() string {
	return "rerank_server"
}

// ============================================================================
// TODO（gRPC 对接，二开点）：
// 待 protoc 生成 rerank gRPC service 代码后，实现 gRPC handler：
//
//   type GRPCService struct {
//       server *Server
//       pb.UnimplementedRerankServiceServer
//   }
//
//   func (g *GRPCService) Rerank(ctx context.Context, req *pb.RerankRequest) (*pb.RerankResponse, error) {
//       domainReq := pbToDomain(req)
//       result, err := g.server.Rerank(ctx, domainReq)
//       if err != nil {
//           return nil, err
//       }
//       return domainToPB(result), nil
//   }
//
// 当前 task 先实现核心逻辑（Rerank/HotReloadModel/HealthCheck），
// gRPC handler 待 protoc 生成代码后对接。
// ============================================================================
