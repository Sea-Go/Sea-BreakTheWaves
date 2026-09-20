package cf

import (
	"errors"
	"math"
	"math/rand"
	"sort"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// MF：基于 ALS（交替最小二乘）的隐因子矩阵分解（Task 7.3）。
// 简化实现：用梯度下降（SGD）近似 ALS，避免复杂矩阵求逆。
// R ~= U * V^T，U 为用户因子矩阵，V 为物品因子矩阵。
//
// 另提供 Retrain/IncrementalTrain 适配方法供 Task 7.4（online.go）定时
// 触发全量/增量训练。
//
// 二开点：
//   - 训练算法：可替换为标准 ALS（闭式解交替优化）或 BPR 等
//   - 因子数：factors 64/128，可按数据规模调整
//   - 正则/学习率：通过 NewMF 参数调优
// ============================================================================

// MF 隐因子矩阵分解模型。
type MF struct {
	// factors 隐因子维度（64 或 128）。
	factors int
	// userFactors 用户因子矩阵：userID -> 因子向量。
	userFactors map[string][]float64
	// itemFactors 物品因子矩阵：articleID -> 因子向量。
	itemFactors map[string][]float64
	// learningRate 学习率。
	learningRate float64
	// reg L2 正则系数。
	reg float64
}

// NewMF 创建 MF 模型。factors 建议 64 或 128。
func NewMF(factors int, lr, reg float64) *MF {
	return &MF{
		factors:      factors,
		userFactors:  make(map[string][]float64),
		itemFactors:  make(map[string][]float64),
		learningRate: lr,
		reg:          reg,
	}
}

// Interaction 用户-文章交互记录。
type Interaction struct {
	// UserID 用户 ID。
	UserID string
	// ArticleID 文章 ID。
	ArticleID string
	// Rating 评分/隐式反馈强度。
	Rating float64
}

// 训练 epoch 默认值，供 Retrain/IncrementalTrain 适配 online.go 使用。
const (
	// defaultRetrainEpochs 全量重训默认 epoch 数。
	defaultRetrainEpochs = 100
	// defaultIncrementalEpochs 增量训练默认 epoch 数。
	defaultIncrementalEpochs = 10
)

// Train 训练模型。
//
// 简化实现：用 SGD 近似 ALS——对每条交互，固定另一侧因子，沿误差梯度更新本侧
// 因子，交替进行。固定随机种子保证可复现。
// TODO(二开): 可替换为标准 ALS（闭式解，对每用户/物品求解线性方程组）。
func (mf *MF) Train(interactions []Interaction, epochs int) error {
	if len(interactions) == 0 {
		return errors.New("mf: interactions is empty")
	}
	if epochs <= 0 {
		return errors.New("mf: epochs must be positive")
	}
	if mf.factors <= 0 {
		return errors.New("mf: factors must be positive")
	}

	// 固定随机种子，保证训练可复现（便于测试）。
	rng := rand.New(rand.NewSource(42))
	// 初始化未见过的用户/物品因子（小随机值，范围 [-0.1, 0.1]）。
	for _, inter := range interactions {
		if _, ok := mf.userFactors[inter.UserID]; !ok {
			mf.userFactors[inter.UserID] = randomVector(mf.factors, rng)
		}
		if _, ok := mf.itemFactors[inter.ArticleID]; !ok {
			mf.itemFactors[inter.ArticleID] = randomVector(mf.factors, rng)
		}
	}

	for epoch := 0; epoch < epochs; epoch++ {
		for _, inter := range interactions {
			uf := mf.userFactors[inter.UserID]
			vf := mf.itemFactors[inter.ArticleID]
			err := inter.Rating - dotProduct(uf, vf)
			// 用旧因子计算梯度后更新（避免顺序更新偏差）。
			for k := 0; k < mf.factors; k++ {
				gradU := err*vf[k] - mf.reg*uf[k]
				gradV := err*uf[k] - mf.reg*vf[k]
				uf[k] += mf.learningRate * gradU
				vf[k] += mf.learningRate * gradV
			}
		}
	}
	return nil
}

// Predict 预测用户对文章的评分（点积）。
// 未知用户或文章返回 0（冷启动）。
func (mf *MF) Predict(userID, articleID string) float64 {
	uf, okU := mf.userFactors[userID]
	vf, okV := mf.itemFactors[articleID]
	if !okU || !okV {
		return 0
	}
	return dotProduct(uf, vf)
}

// Recommend 为用户推荐文章，按预测分降序取 Top-K。
func (mf *MF) Recommend(userID string, candidateArticleIDs []string, topK int) []domain.Candidate {
	cands := make([]domain.Candidate, 0, len(candidateArticleIDs))
	for _, aid := range candidateArticleIDs {
		s := mf.Predict(userID, aid)
		cands = append(cands, domain.Candidate{
			ArticleID: aid,
			Score:     s,
			Source:    "cf",
			Scores:    map[string]float64{"cf_score": s},
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].Score > cands[j].Score
	})
	if topK > 0 && len(cands) > topK {
		cands = cands[:topK]
	}
	return cands
}

// RMSE 计算给定交互集的均方根误差。
func (mf *MF) RMSE(interactions []Interaction) float64 {
	if len(interactions) == 0 {
		return 0
	}
	var sumSq float64
	for _, inter := range interactions {
		d := inter.Rating - mf.Predict(inter.UserID, inter.ArticleID)
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(interactions)))
}

// Retrain 全量重训（Task 7.4 online.go 调用）。
// 委托 Train 以默认 epoch 数训练；返回训练错误。
func (mf *MF) Retrain(interactions []Interaction) error {
	return mf.Train(interactions, defaultRetrainEpochs)
}

// IncrementalTrain 增量训练（Task 7.4 online.go 调用）。
// 以较少 epoch 基于新交互微调因子（已存在的用户/物品因子保留，仅更新）。
func (mf *MF) IncrementalTrain(interactions []Interaction) error {
	return mf.Train(interactions, defaultIncrementalEpochs)
}

// randomVector 生成小随机值向量（范围 [-0.1, 0.1]）。
func randomVector(n int, r *rand.Rand) []float64 {
	v := make([]float64, n)
	for i := range v {
		v[i] = (r.Float64() - 0.5) * 0.2
	}
	return v
}

// dotProduct 计算两向量点积。
func dotProduct(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
