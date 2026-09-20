package rerank

import (
	"context"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// TestTwoTower_EmbedUser_Stub 验证用户塔 nil 时返回确定性 stub 向量。
func TestTwoTower_EmbedUser_Stub(t *testing.T) {
	tt := NewTwoTower(nil, nil)
	profile := domain.UserProfile{
		Key:    domain.UserKey{UserID: "u1"},
		Static: &domain.StaticProfile{Interests: []string{"AI", "tech"}},
	}
	vec, err := tt.EmbedUser(context.Background(), profile)
	if err != nil {
		t.Fatalf("EmbedUser 错误: %v", err)
	}
	if len(vec) != tt.dim {
		t.Fatalf("向量维度期望 %d, 实际 %d", tt.dim, len(vec))
	}
	// stub 向量应 L2 归一化
	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	if norm <= 0.99 || norm >= 1.01 {
		t.Errorf("stub 向量应归一化, norm^2 = %v", norm)
	}
}

// TestTwoTower_EmbedItem_Stub 验证物品塔 nil 时返回 stub 向量。
func TestTwoTower_EmbedItem_Stub(t *testing.T) {
	tt := NewTwoTower(nil, nil)
	c := domain.Candidate{
		ArticleID: "art-1",
		Extra:     map[string]any{"title": "AI", "tags": []string{"AI"}},
	}
	vec, err := tt.EmbedItem(context.Background(), c)
	if err != nil {
		t.Fatalf("EmbedItem 错误: %v", err)
	}
	if len(vec) != tt.dim {
		t.Fatalf("向量维度期望 %d, 实际 %d", tt.dim, len(vec))
	}
}

// TestTwoTower_EmbedDeterministic 验证 stub 向量的确定性（同输入同输出）。
func TestTwoTower_EmbedDeterministic(t *testing.T) {
	tt := NewTwoTower(nil, nil)
	profile := domain.UserProfile{Key: domain.UserKey{UserID: "u1"}}
	v1, _ := tt.EmbedUser(context.Background(), profile)
	v2, _ := tt.EmbedUser(context.Background(), profile)
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("EmbedUser 应确定性, 位置 %d 不同 (%v vs %v)", i, v1[i], v2[i])
		}
	}
}

// TestTwoTower_WithModel 验证模型加载后走 Predict 路径调制向量。
func TestTwoTower_WithModel(t *testing.T) {
	m := &stubModel{predict: func(input []float64) (float64, error) {
		return 0.3, nil
	}}
	tt := NewTwoTower(m, m)
	profile := domain.UserProfile{Key: domain.UserKey{UserID: "u1"}}

	uVec, err := tt.EmbedUser(context.Background(), profile)
	if err != nil {
		t.Fatalf("EmbedUser 错误: %v", err)
	}
	if len(uVec) != tt.dim {
		t.Fatalf("向量维度期望 %d, 实际 %d", tt.dim, len(uVec))
	}
	if m.calls != 1 {
		t.Errorf("EmbedUser 后 Predict 调用次数期望 1, 实际 %d", m.calls)
	}
	// 模型调制后向量应受 0.3 影响（非纯 stub）
	stubU, _ := NewTwoTower(nil, nil).EmbedUser(context.Background(), profile)
	different := false
	for i := range uVec {
		if uVec[i] != stubU[i] {
			different = true
			break
		}
	}
	if !different {
		t.Errorf("模型调制后向量应与纯 stub 不同")
	}
}

// TestTwoTower_Score 验证点积评分在 [-1,1]。
func TestTwoTower_Score(t *testing.T) {
	tt := NewTwoTower(nil, nil)
	profile := domain.UserProfile{Key: domain.UserKey{UserID: "u1"}}
	c := domain.Candidate{ArticleID: "art-1"}
	score, err := tt.Score(context.Background(), profile, c)
	if err != nil {
		t.Fatalf("Score 错误: %v", err)
	}
	if score < -1 || score > 1 {
		t.Errorf("点积应在 [-1,1], 实际 %v", score)
	}
}

// TestTwoTower_Rerank 验证批量重排与 topK 截断、降序。
func TestTwoTower_Rerank(t *testing.T) {
	tt := NewTwoTower(nil, nil)
	profile := domain.UserProfile{Key: domain.UserKey{UserID: "u1"}}
	candidates := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
	}
	out, err := tt.Rerank(context.Background(), profile, candidates, 2)
	if err != nil {
		t.Fatalf("Rerank 错误: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("Rerank 返回数量期望 2, 实际 %d", len(out))
	}
	if out[0].Score < out[1].Score {
		t.Errorf("应按分数降序, out[0]=%v < out[1]=%v", out[0].Score, out[1].Score)
	}
}
