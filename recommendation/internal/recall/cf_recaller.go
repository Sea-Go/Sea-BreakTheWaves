package recall

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"sea/internal/cf"
	"sea/internal/domain"
)

// ============================================================================
// 该文件实现 CFRecaller（协同过滤召回器），实现 domain.Recaller interface。
// Task 7.6：填充 Phase 3 stub，接入 User-CF/Item-CF/MF 三路 CF 召回 + 冷启动。
//
// 召回流程：
//  1. 冷启动判断：用户无历史行为 → 调 coldStart.ColdStartUser
//  2. 正常路径：并行调 UserCFEngine/ItemCFEngine/MFEngine 的 Recommend
//  3. 分数融合：三种 CF 分数加权 sum，存入 Candidate.Scores map
//     （cf_user / cf_item / cf_mf / cf_fused 四个键）
//  4. 按 cf_fused 倒序排序，截断到 topK 返回
//
// 设计说明：
//   - 三路 CF 引擎均通过 interface 抽象（UserCFEngine/ItemCFEngine/MFEngine），
//     CFRecaller 不直接依赖 cf.UserCF/cf.ItemCF/cf.MF 具体类型，便于二开替换。
//   - 提供适配器（UserCFEngineAdapter/ItemCFEngineAdapter/MFEngineAdapter）包装
//     并行 agent 实现的具体类型，适配器负责从 RecallRequest 提取所需参数。
//
// 二开扩展点：
//   - 替换融合权重（cfUserWeight/cfItemWeight/cfMFWeight）改变三路 CF 贡献
//   - 实现 UserCFEngine/ItemCFEngine/MFEngine interface 注入自研 CF 算法
//   - 调整冷启动判定阈值（如要求最小点击数才走正常路径）
// ============================================================================

// 三种 CF 分数融合权重（二开点：可按业务调整三路 CF 贡献比例）。
const (
	// cfUserWeight User-CF 融合权重。
	cfUserWeight = 0.4
	// cfItemWeight Item-CF 融合权重。
	cfItemWeight = 0.3
	// cfMFWeight MF 矩阵分解融合权重。
	cfMFWeight = 0.3
)

// Candidate.Scores map 的键名。
const (
	scoreKeyCFUser  = "cf_user"  // User-CF 分数
	scoreKeyCFItem  = "cf_item"  // Item-CF 分数
	scoreKeyCFMF    = "cf_mf"    // MF 分数
	scoreKeyCFFused = "cf_fused" // 三路融合后的 CF 分数
)

// ----------------------------------------------------------------------------
// CF 引擎 interface（二开核心扩展点）
// ----------------------------------------------------------------------------

// UserCFEngine User-CF 召回引擎接口。
// 二开：实现该 interface 注入自研 User-CF 算法（如基于图嵌入的相似用户召回）。
type UserCFEngine interface {
	// Recommend 基于 RecallRequest 返回 User-CF 候选。
	Recommend(ctx context.Context, req domain.RecallRequest, topK int) []domain.Candidate
}

// ItemCFEngine Item-CF 召回引擎接口。
// 二开：实现该 interface 注入自研 Item-CF 算法（如加入时间衰减的共现加权）。
type ItemCFEngine interface {
	// Recommend 基于 RecallRequest 返回 Item-CF 候选。
	Recommend(ctx context.Context, req domain.RecallRequest, topK int) []domain.Candidate
}

// MFEngine MF 矩阵分解召回引擎接口。
// 二开：实现该 interface 注入自研 MF 算法（如 BPR/DSSM/双塔模型）。
type MFEngine interface {
	// Recommend 基于 RecallRequest 返回 MF 候选。
	Recommend(ctx context.Context, req domain.RecallRequest, topK int) []domain.Candidate
}

// ----------------------------------------------------------------------------
// CFRecaller 召回器
// ----------------------------------------------------------------------------

// CFRecaller 协同过滤召回器，融合 User-CF/Item-CF/MF 三路召回 + 冷启动。
// 实现 domain.Recaller interface，属于 fast / hybrid 路径，零 LLM token。
type CFRecaller struct {
	userCF    UserCFEngine
	itemCF    ItemCFEngine
	mf        MFEngine
	coldStart *cf.ColdStart
	topK      int
}

// NewCFRecaller 创建协同过滤召回器。
// userCF User-CF 引擎；itemCF Item-CF 引擎；mf 矩阵分解引擎；
// coldStart 冷启动策略；topK 默认召回数量上限（req.TopK>0 时优先使用 req.TopK）。
// 各引擎参数传 nil 时跳过该路召回（降级运行，适用于测试或部分部署场景）。
func NewCFRecaller(userCF UserCFEngine, itemCF ItemCFEngine, mf MFEngine, coldStart *cf.ColdStart, topK int) *CFRecaller {
	return &CFRecaller{
		userCF:    userCF,
		itemCF:    itemCF,
		mf:        mf,
		coldStart: coldStart,
		topK:      topK,
	}
}

// Recall 执行协同过滤召回，实现 domain.Recaller.Recall。
//
// 流程：
//  1. 冷启动判断：用户无历史行为 → 调 coldStart.ColdStartUser
//  2. 并行调 UserCFEngine/ItemCFEngine/MFEngine 的 Recommend
//  3. 三种 CF 分数融合（加权 sum），存入 Candidate.Scores map
//  4. 按 cf_fused 倒序排序，截断到 topK 返回
//
// 返回 RecallResult，Source 为 "cf"。
func (r *CFRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	topK := r.topK
	if req.TopK > 0 {
		topK = req.TopK
	}

	// 1. 冷启动判断：用户无历史行为时走冷启动路径。
	if isColdStartUser(req) {
		if r.coldStart == nil {
			return domain.RecallResult{}, errors.New("cf recaller: 冷启动但未注入 ColdStart 策略")
		}
		profile := req.Profile
		if profile == nil {
			profile = &domain.UserProfile{}
		}
		cands, err := r.coldStart.ColdStartUser(ctx, *profile)
		if err != nil {
			return domain.RecallResult{}, fmt.Errorf("cf recaller: 冷启动召回失败: %w", err)
		}
		if topK > 0 && len(cands) > topK {
			cands = cands[:topK]
		}
		return domain.RecallResult{
			Candidates: cands,
			Source:     string(domain.RecallSourceCF),
		}, nil
	}

	// 2. 正常路径：并行调三路 CF 召回。
	var (
		userCands, itemCands, mfCands []domain.Candidate
	)
	wg := sync.WaitGroup{}
	if r.userCF != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			userCands = r.userCF.Recommend(ctx, req, topK)
		}()
	}
	if r.itemCF != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			itemCands = r.itemCF.Recommend(ctx, req, topK)
		}()
	}
	if r.mf != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mfCands = r.mf.Recommend(ctx, req, topK)
		}()
	}
	wg.Wait()

	// 3. 三种 CF 分数融合（加权 sum）。
	fused := r.fuseCFScores(userCands, itemCands, mfCands)

	// 4. 截断到 topK。
	if topK > 0 && len(fused) > topK {
		fused = fused[:topK]
	}
	return domain.RecallResult{
		Candidates: fused,
		Source:     string(domain.RecallSourceCF),
	}, nil
}

// Name 返回召回器名称，实现 domain.Recaller.Name。
func (r *CFRecaller) Name() string {
	return string(domain.RecallSourceCF)
}

// fuseCFScores 融合三路 CF 召回结果，加权求和得到 cf_fused 分数。
// 将 cf_user / cf_item / cf_mf / cf_fused 四个分数写入 Candidate.Scores map。
//
// 二开扩展点：替换融合公式（如改为 RRF/归一化加权/最大值），或调整权重。
func (r *CFRecaller) fuseCFScores(userCands, itemCands, mfCands []domain.Candidate) []domain.Candidate {
	m := make(map[string]*cfEntry)

	// 收集 User-CF 分数。
	for _, c := range userCands {
		if c.ArticleID == "" {
			continue
		}
		e := getOrCreateCFEntry(m, c.ArticleID, c)
		e.cfUser = c.Score
		e.hasAny = true
	}
	// 收集 Item-CF 分数。
	for _, c := range itemCands {
		if c.ArticleID == "" {
			continue
		}
		e := getOrCreateCFEntry(m, c.ArticleID, c)
		e.cfItem = c.Score
		e.hasAny = true
	}
	// 收集 MF 分数。
	for _, c := range mfCands {
		if c.ArticleID == "" {
			continue
		}
		e := getOrCreateCFEntry(m, c.ArticleID, c)
		e.cfMF = c.Score
		e.hasAny = true
	}

	// 计算融合分数并组装结果。
	result := make([]domain.Candidate, 0, len(m))
	for _, e := range m {
		if !e.hasAny {
			continue
		}
		fused := cfUserWeight*e.cfUser + cfItemWeight*e.cfItem + cfMFWeight*e.cfMF
		if e.cand.Scores == nil {
			e.cand.Scores = make(map[string]float64)
		}
		e.cand.Scores[scoreKeyCFUser] = e.cfUser
		e.cand.Scores[scoreKeyCFItem] = e.cfItem
		e.cand.Scores[scoreKeyCFMF] = e.cfMF
		e.cand.Scores[scoreKeyCFFused] = fused
		e.cand.Score = fused
		e.cand.Source = string(domain.RecallSourceCF)
		result = append(result, e.cand)
	}

	// 按 cf_fused 分数倒序排序。
	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})
	return result
}

// getOrCreateCFEntry 从 map 中取或创建条目。
func getOrCreateCFEntry(m map[string]*cfEntry, articleID string, cand domain.Candidate) *cfEntry {
	if e, ok := m[articleID]; ok {
		return e
	}
	e := &cfEntry{cand: cand}
	m[articleID] = e
	return e
}

// cfEntry 分数融合中间条目。
type cfEntry struct {
	cand   domain.Candidate
	cfUser float64
	cfItem float64
	cfMF   float64
	hasAny bool
}

// isColdStartUser 判断是否为冷启动用户（无历史行为）。
// 冷启动条件：画像缺失，或行为画像中无任何点击/点赞/收藏/完成阅读记录。
func isColdStartUser(req domain.RecallRequest) bool {
	if req.Profile == nil {
		return true
	}
	b := req.Profile.Behavior
	if b == nil {
		return true
	}
	return len(b.RecentClicks) == 0 &&
		len(b.RecentLikes) == 0 &&
		len(b.RecentFavorites) == 0 &&
		len(b.RecentCompletions) == 0
}

// ----------------------------------------------------------------------------
// 适配器：包装并行 agent 实现的 cf.UserCF/cf.ItemCF/cf.MF 为 CF 引擎 interface
// ----------------------------------------------------------------------------

// UserCFEngineAdapter 包装 cf.UserCF 为 UserCFEngine。
// 从 RecallRequest.Profile.Behavior 构造目标用户行为，与注入的相似用户列表
// 一起传入 cf.UserCF.Recommend。
//
// 二开点：similarUsers 为静态注入的相似用户行为列表。
// 真实场景应替换为动态相似用户查询（如实现 SimilarUserRepo interface 从
// Redis/Postgres 拉取在线相似用户），再包装为 UserCFEngine。
type UserCFEngineAdapter struct {
	ucf          *cf.UserCF
	similarUsers []cf.UserBehavior
}

// NewUserCFEngine 创建 User-CF 引擎适配器。
// ucf User-CF 引擎；similarUsers 相似用户行为列表（二开：可替换为动态查询）。
func NewUserCFEngine(ucf *cf.UserCF, similarUsers []cf.UserBehavior) *UserCFEngineAdapter {
	return &UserCFEngineAdapter{ucf: ucf, similarUsers: similarUsers}
}

// Recommend 实现 UserCFEngine interface。
// 从 req.Profile.Behavior 构造目标用户行为，调 cf.UserCF.Recommend。
func (a *UserCFEngineAdapter) Recommend(_ context.Context, req domain.RecallRequest, topK int) []domain.Candidate {
	if a.ucf == nil {
		return nil
	}
	target := buildUserBehavior(req)
	return a.ucf.Recommend(target, a.similarUsers, topK)
}

// ItemCFEngineAdapter 包装 cf.ItemCF 为 ItemCFEngine。
// 从 RecallRequest.Profile.Behavior 提取用户已看文章列表，与注入的共现矩阵
// 一起传入 cf.ItemCF.Recommend。
//
// 二开点：matrix 由 OnlineCF 实时维护，生产部署时应与 OnlineCF 共享同一实例。
type ItemCFEngineAdapter struct {
	icf    *cf.ItemCF
	matrix *cf.CoOccurrenceMatrix
}

// NewItemCFEngine 创建 Item-CF 引擎适配器。
// icf Item-CF 引擎；matrix 共现矩阵（由 OnlineCF 实时维护，二开：可替换为分布式缓存）。
func NewItemCFEngine(icf *cf.ItemCF, matrix *cf.CoOccurrenceMatrix) *ItemCFEngineAdapter {
	return &ItemCFEngineAdapter{icf: icf, matrix: matrix}
}

// Recommend 实现 ItemCFEngine interface。
// 从 req.Profile.Behavior 提取已看文章列表，调 cf.ItemCF.Recommend。
func (a *ItemCFEngineAdapter) Recommend(_ context.Context, req domain.RecallRequest, topK int) []domain.Candidate {
	if a.icf == nil || a.matrix == nil {
		return nil
	}
	articleIDs := buildArticleHistory(req)
	if len(articleIDs) == 0 {
		return nil
	}
	return a.icf.Recommend(articleIDs, a.matrix, topK)
}

// MFEngineAdapter 包装 cf.MF 为 MFEngine。
// 使用注入的候选文章池作为 MF.Recommend 的 candidateArticleIDs。
//
// 二开点：candidatePool 为静态候选池。
// 真实场景应替换为动态候选生成（如从倒排索引/向量库召回候选文章 ID），
// 或使用 Item-CF 结果作为 MF 候选（漏斗式召回）。
type MFEngineAdapter struct {
	mf            *cf.MF
	candidatePool []string
}

// NewMFEngine 创建 MF 引擎适配器。
// mf 矩阵分解模型；candidatePool 候选文章 ID 池（二开：可替换为动态候选生成）。
func NewMFEngine(mf *cf.MF, candidatePool []string) *MFEngineAdapter {
	return &MFEngineAdapter{mf: mf, candidatePool: candidatePool}
}

// Recommend 实现 MFEngine interface。
// 调 cf.MF.Recommend 对候选池文章预测评分。
func (a *MFEngineAdapter) Recommend(_ context.Context, req domain.RecallRequest, topK int) []domain.Candidate {
	if a.mf == nil || len(a.candidatePool) == 0 {
		return nil
	}
	return a.mf.Recommend(req.UserKey.UserID, a.candidatePool, topK)
}

// buildUserBehavior 从 RecallRequest 构造 cf.UserBehavior。
// 将用户近期点击/点赞/收藏/完成阅读文章汇总为 ArticleIDs。
func buildUserBehavior(req domain.RecallRequest) cf.UserBehavior {
	bh := cf.UserBehavior{UserID: req.UserKey.UserID}
	if req.Profile == nil || req.Profile.Behavior == nil {
		return bh
	}
	b := req.Profile.Behavior
	bh.ArticleIDs = make([]string, 0, len(b.RecentClicks)+len(b.RecentLikes)+len(b.RecentFavorites)+len(b.RecentCompletions))
	bh.ArticleIDs = append(bh.ArticleIDs, b.RecentClicks...)
	bh.ArticleIDs = append(bh.ArticleIDs, b.RecentLikes...)
	bh.ArticleIDs = append(bh.ArticleIDs, b.RecentFavorites...)
	bh.ArticleIDs = append(bh.ArticleIDs, b.RecentCompletions...)
	return bh
}

// buildArticleHistory 从 RecallRequest 提取用户已看文章 ID 列表（去重）。
func buildArticleHistory(req domain.RecallRequest) []string {
	if req.Profile == nil || req.Profile.Behavior == nil {
		return nil
	}
	b := req.Profile.Behavior
	seen := make(map[string]struct{})
	var arts []string
	for _, aid := range b.RecentClicks {
		if aid != "" {
			if _, ok := seen[aid]; !ok {
				seen[aid] = struct{}{}
				arts = append(arts, aid)
			}
		}
	}
	for _, aid := range b.RecentLikes {
		if aid != "" {
			if _, ok := seen[aid]; !ok {
				seen[aid] = struct{}{}
				arts = append(arts, aid)
			}
		}
	}
	for _, aid := range b.RecentFavorites {
		if aid != "" {
			if _, ok := seen[aid]; !ok {
				seen[aid] = struct{}{}
				arts = append(arts, aid)
			}
		}
	}
	for _, aid := range b.RecentCompletions {
		if aid != "" {
			if _, ok := seen[aid]; !ok {
				seen[aid] = struct{}{}
				arts = append(arts, aid)
			}
		}
	}
	return arts
}

// 编译期断言：*CFRecaller 实现 domain.Recaller interface。
var _ domain.Recaller = (*CFRecaller)(nil)

// 编译期断言：适配器实现对应 CF 引擎 interface。
var (
	_ UserCFEngine = (*UserCFEngineAdapter)(nil)
	_ ItemCFEngine = (*ItemCFEngineAdapter)(nil)
	_ MFEngine     = (*MFEngineAdapter)(nil)
)
