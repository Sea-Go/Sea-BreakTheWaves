package rerank

import (
	"context"
	"hash/fnv"
	"math"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// two_tower.go 实现双塔召回模型（用户塔 + 物品塔），通过点积评分。
// Model 用 interface 抽象（不直接 import ONNX），确保离线编译。
// 真实双塔 embedding 应扩展 Model 接口增加 Embed 方法（TODO 标注）。
// ============================================================================

// TwoTower 双塔召回模型（用户塔 + 物品塔），通过点积评分。
// 二开扩展点：替换 userTower/itemTower 的 Model 实现接入真实双塔模型；
// 实际 Embedding 应扩展 Model 接口增加 Embed([]float64)([]float64,error) 方法（TODO）。
type TwoTower struct {
	userTower Model // 用户塔模型
	itemTower Model // 物品塔模型
	dim       int   // embedding 维度
}

// NewTwoTower 创建双塔模型，userTower/itemTower 为两塔模型（可为 nil，走 stub）。
func NewTwoTower(userTower, itemTower Model) *TwoTower {
	return &TwoTower{
		userTower: userTower,
		itemTower: itemTower,
		dim:       64, // 默认 embedding 维度（二开可调整）
	}
}

// EmbedUser 用户塔向量化。
// ctx 上下文；profile 用户画像。
// 若 userTower 为 nil，返回基于画像的确定性 stub 向量（TODO 集成 ONNX 运行时）。
func (tt *TwoTower) EmbedUser(ctx context.Context, profile domain.UserProfile) ([]float64, error) {
	_ = ctx
	vec := stubUserVector(profile, tt.dim)
	if tt.userTower == nil {
		// TODO: 集成 ONNX/GGUF 运行时加载真实用户塔模型
		return vec, nil
	}
	// 模型已加载：用 Predict 标量调制 stub 向量
	// 二开点：实际应扩展 Model 接口增加 Embed 方法返回向量
	feats := buildUserFeatureInput(profile, tt.dim)
	s, err := tt.userTower.Predict(feats)
	if err != nil {
		return nil, err
	}
	for i := range vec {
		vec[i] = vec[i]*0.5 + s*0.5
	}
	return vec, nil
}

// EmbedItem 物品塔向量化。
// ctx 上下文；candidate 候选文章。
// 若 itemTower 为 nil，返回基于候选的确定性 stub 向量（TODO 集成 ONNX 运行时）。
func (tt *TwoTower) EmbedItem(ctx context.Context, candidate domain.Candidate) ([]float64, error) {
	_ = ctx
	vec := stubItemVector(candidate, tt.dim)
	if tt.itemTower == nil {
		// TODO: 集成 ONNX/GGUF 运行时加载真实物品塔模型
		return vec, nil
	}
	// 模型已加载：用 Predict 标量调制 stub 向量
	feats := buildItemFeatureInput(candidate, tt.dim)
	s, err := tt.itemTower.Predict(feats)
	if err != nil {
		return nil, err
	}
	for i := range vec {
		vec[i] = vec[i]*0.5 + s*0.5
	}
	return vec, nil
}

// Score 点积评分：dot(EmbedUser, EmbedItem)。
func (tt *TwoTower) Score(ctx context.Context, profile domain.UserProfile, candidate domain.Candidate) (float64, error) {
	uVec, err := tt.EmbedUser(ctx, profile)
	if err != nil {
		return 0, err
	}
	iVec, err := tt.EmbedItem(ctx, candidate)
	if err != nil {
		return 0, err
	}
	return dotProduct(uVec, iVec), nil
}

// Rerank 批量重排，返回按分数降序的 topK 候选。
// ctx 上下文；profile 用户画像；candidates 候选列表；topK 返回数量（<=0 表示不截断）。
func (tt *TwoTower) Rerank(ctx context.Context, profile domain.UserProfile, candidates []domain.Candidate, topK int) ([]domain.Candidate, error) {
	uVec, err := tt.EmbedUser(ctx, profile)
	if err != nil {
		return nil, err
	}
	scored := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		iVec, err := tt.EmbedItem(ctx, c)
		if err != nil {
			return nil, err
		}
		c.Score = dotProduct(uVec, iVec)
		scored = append(scored, c)
	}
	sortByScoreDesc(scored)
	if topK > 0 && topK < len(scored) {
		scored = scored[:topK]
	}
	return scored, nil
}

// ---- 辅助函数：stub 向量 ----

// stubUserVector 基于用户画像的确定性 stub 向量（hash + 兴趣调制，L2 归一化）。
func stubUserVector(profile domain.UserProfile, dim int) []float64 {
	vec := hashVector(profile.Key.UserID, dim)
	interests := userInterests(profile)
	for i := range vec {
		vec[i] += float64(len(interests)) * 0.01
	}
	normalize(vec)
	return vec
}

// stubItemVector 基于候选文章的确定性 stub 向量（hash + 标签/标题调制，L2 归一化）。
func stubItemVector(c domain.Candidate, dim int) []float64 {
	vec := hashVector(c.ArticleID, dim)
	tags := stringSliceField(c, "tags")
	title := stringField(c, "title")
	for i := range vec {
		vec[i] += float64(len(tags))*0.01 + float64(len(title))*0.001
	}
	normalize(vec)
	return vec
}

// buildUserFeatureInput 构造用户塔模型输入向量（供 Predict 使用）。
func buildUserFeatureInput(profile domain.UserProfile, dim int) []float64 {
	vec := hashVector(profile.Key.UserID, dim)
	interests := userInterests(profile)
	for i := range vec {
		vec[i] += float64(len(interests)) * 0.01
	}
	return vec
}

// buildItemFeatureInput 构造物品塔模型输入向量。
func buildItemFeatureInput(c domain.Candidate, dim int) []float64 {
	return hashVector(c.ArticleID, dim)
}

// ---- 辅助函数：向量运算 ----

// dotProduct 计算两个向量的点积。
func dotProduct(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

// normalize L2 归一化向量（原地修改）。
func normalize(vec []float64) {
	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		return
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] /= norm
	}
}

// hashVector 将字符串确定性映射为 dim 维向量（基于 FNV-1a hash，值域 [-1,1)）。
func hashVector(s string, dim int) []float64 {
	vec := make([]float64, dim)
	if dim <= 0 {
		return vec
	}
	for i := 0; i < dim; i++ {
		h := fnv.New64a()
		h.Write([]byte(s))
		h.Write([]byte(strconv.Itoa(i)))
		val := h.Sum64()
		vec[i] = (float64(val%2000) / 1000.0) - 1.0
	}
	return vec
}
