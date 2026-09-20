package recall

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 RuleRecaller，基于业务规则的召回器（热点/最新/编辑精选）。
// 实现 domain.Recaller interface，依赖 Repository interface 抽象数据源
// （通常由 internal/repo 的 Postgres 实现注入），零 LLM token，属于
// fast / hybrid 路径。
//
// 规则选择策略（根据 req.Intent）：
//   - Intent.Signals["rule"] == "hot"     → 热点（按点击数排序）
//   - Intent.Signals["rule"] == "latest"  → 最新（按时间排序）
//   - Intent.Signals["rule"] == "editor"  → 编辑精选（白名单）
//   - Intent.TimeIntent == "latest"       → 最新
//   - 其他 / 无 Intent                     → 默认热点
//
// 二开扩展点：实现 Repository interface 注入业务规则数据源（如活动期间
// 提升活动文章权重），或实现 domain.Recaller 注册为独立召回器。
// ============================================================================

// Repository 规则召回的仓储抽象，提供三类业务规则数据查询。
//
// 二开说明：实现该 interface 注入自定义数据源（如 Postgres/Redis/ES），
// 默认实现通常位于 internal/repo 包。通过依赖注入替换即可改变规则召回
// 的数据来源，无需修改 RuleRecaller 本身。
type Repository interface {
	// ListHotArticles 列出热点文章（按点击数倒序）。
	ListHotArticles(ctx context.Context, topK int) ([]domain.Candidate, error)
	// ListLatestArticles 列出最新文章（按发布时间倒序）。
	ListLatestArticles(ctx context.Context, topK int) ([]domain.Candidate, error)
	// ListEditorPicks 列出编辑精选文章（白名单）。
	ListEditorPicks(ctx context.Context, topK int) ([]domain.Candidate, error)
}

// RuleRecaller 规则召回器，根据意图选择热点/最新/编辑精选规则召回候选文章。
type RuleRecaller struct {
	repo Repository
	topK int
}

// NewRuleRecaller 创建规则召回器。
// repo 为 Repository 实现（如 internal/repo 的 Postgres 仓储）；
// topK 为默认召回数量上限（req.TopK>0 时优先使用 req.TopK）。
func NewRuleRecaller(repo Repository, topK int) *RuleRecaller {
	return &RuleRecaller{repo: repo, topK: topK}
}

// Recall 执行规则召回，实现 domain.Recaller.Recall。
//
// 规则选择顺序：
//  1. req.Intent.Signals["rule"] 显式指定（hot/latest/editor）
//  2. req.Intent.TimeIntent == "latest" → 最新
//  3. 默认 → 热点
//
// 返回 RecallResult，Source 为 "rule"。候选 Source 标记为具体规则
// （hot/latest/editor）。
func (r *RuleRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	topK := r.topK
	if req.TopK > 0 {
		topK = req.TopK
	}

	rule := r.selectRule(req)
	var cands []domain.Candidate
	var err error
	switch rule {
	case "latest":
		cands, err = r.repo.ListLatestArticles(ctx, topK)
	case "editor":
		cands, err = r.repo.ListEditorPicks(ctx, topK)
	default: // hot
		cands, err = r.repo.ListHotArticles(ctx, topK)
	}
	if err != nil {
		return domain.RecallResult{}, err
	}

	// 标记候选来源为具体规则，便于下游追踪与调试。
	for i := range cands {
		if cands[i].Source == "" {
			cands[i].Source = rule
		}
	}

	// 截断到 topK（仓储可能返回超额）。
	if topK > 0 && len(cands) > topK {
		cands = cands[:topK]
	}
	return domain.RecallResult{
		Candidates: cands,
		Source:     string(domain.RecallSourceRule),
	}, nil
}

// Name 返回召回器名称，实现 domain.Recaller.Name。
func (r *RuleRecaller) Name() string {
	return string(domain.RecallSourceRule)
}

// selectRule 根据召回请求的意图选择规则类型（hot/latest/editor）。
func (r *RuleRecaller) selectRule(req domain.RecallRequest) string {
	if req.Intent != nil {
		// 显式 Signal 优先。
		if rule, ok := req.Intent.Signals["rule"]; ok {
			switch rule {
			case "hot", "latest", "editor":
				return rule
			}
		}
		// 时效意图为 latest 时走最新规则。
		if req.Intent.TimeIntent == "latest" {
			return "latest"
		}
	}
	return "hot"
}
