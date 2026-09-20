package cf

import (
	"math"
	"testing"
)

func TestUserCFVectorize(t *testing.T) {
	ucf := NewUserCF(10, 0.0)
	// 点击计数模式
	vec := ucf.Vectorize(UserBehavior{
		ArticleIDs: []string{"a", "a", "b"},
	})
	if vec["a"] != 2 || vec["b"] != 1 {
		t.Fatalf("expected a=2 b=1, got %v", vec)
	}
	// 显式权重覆盖点击计数
	vec2 := ucf.Vectorize(UserBehavior{
		ArticleIDs: []string{"a", "b"},
		Weights:    map[string]float64{"a": 5.0},
	})
	if vec2["a"] != 5.0 || vec2["b"] != 1 {
		t.Fatalf("expected a=5 b=1, got %v", vec2)
	}
}

func TestUserCFCosineSimilarity(t *testing.T) {
	ucf := NewUserCF(10, 0.0)
	// 完全相同 -> 1
	if s := ucf.CosineSimilarity(
		map[string]float64{"a": 1, "b": 1},
		map[string]float64{"a": 1, "b": 1},
	); math.Abs(s-1) > 1e-9 {
		t.Fatalf("identical vectors should have similarity 1, got %v", s)
	}
	// 完全正交 -> 0
	if s := ucf.CosineSimilarity(
		map[string]float64{"a": 1},
		map[string]float64{"b": 1},
	); s != 0 {
		t.Fatalf("orthogonal vectors should have similarity 0, got %v", s)
	}
	// 空向量 -> 0
	if s := ucf.CosineSimilarity(
		map[string]float64{},
		map[string]float64{"a": 1},
	); s != 0 {
		t.Fatalf("empty vector similarity should be 0, got %v", s)
	}
	// 已知值：cos = (1*1)/(sqrt(2)*1) = 1/sqrt2
	got := ucf.CosineSimilarity(
		map[string]float64{"a": 1, "b": 1},
		map[string]float64{"a": 1},
	)
	want := 1.0 / math.Sqrt2
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestUserCFFindSimilarUsers(t *testing.T) {
	ucf := NewUserCF(10, 0.1)
	target := UserBehavior{UserID: "u0", ArticleIDs: []string{"a", "b"}}
	candidates := []UserBehavior{
		{UserID: "u1", ArticleIDs: []string{"a", "b", "c"}}, // 高相似
		{UserID: "u2", ArticleIDs: []string{"c", "d"}},      // 0 相似，被过滤
		{UserID: "u3", ArticleIDs: []string{"a", "e"}},      // 中等相似
	}
	sims := ucf.FindSimilarUsers(target, candidates, 2)
	if len(sims) != 2 {
		t.Fatalf("expected 2 similar users, got %d", len(sims))
	}
	if sims[0].UserID != "u1" {
		t.Fatalf("expected top similar user u1, got %s", sims[0].UserID)
	}
	// 应按相似度降序排列
	if sims[0].Similarity < sims[1].Similarity {
		t.Fatalf("similar users should be sorted desc")
	}
	// 自身应被排除
	targetSelf := UserBehavior{UserID: "u0", ArticleIDs: []string{"a", "b"}}
	candidatesWithSelf := []UserBehavior{
		targetSelf,
		{UserID: "u1", ArticleIDs: []string{"a", "b"}},
	}
	sims2 := ucf.FindSimilarUsers(targetSelf, candidatesWithSelf, 5)
	for _, s := range sims2 {
		if s.UserID == "u0" {
			t.Fatalf("target user should be excluded from similar users")
		}
	}
}

func TestUserCFRecommend(t *testing.T) {
	ucf := NewUserCF(10, 0.0)
	target := UserBehavior{UserID: "u0", ArticleIDs: []string{"a", "b"}}
	similar := []UserBehavior{
		{UserID: "u1", ArticleIDs: []string{"a", "b", "c"}, Weights: map[string]float64{"c": 2.0}},
		{UserID: "u2", ArticleIDs: []string{"a", "c", "d"}},
	}
	cands := ucf.Recommend(target, similar, 5)
	// 不应包含已看文章 a/b
	for _, c := range cands {
		if c.ArticleID == "a" || c.ArticleID == "b" {
			t.Fatalf("should not recommend seen article %s", c.ArticleID)
		}
	}
	// c 应排在首位（两个相似用户都看过，累加分最高）
	if len(cands) == 0 || cands[0].ArticleID != "c" {
		t.Fatalf("expected top recommendation c, got %v", cands)
	}
	// Source 应为 cf
	if cands[0].Source != "cf" {
		t.Fatalf("expected source cf, got %s", cands[0].Source)
	}
	// Scores 应含 cf_score
	if cands[0].Scores["cf_score"] != cands[0].Score {
		t.Fatalf("Scores[cf_score] should equal Score")
	}
}
