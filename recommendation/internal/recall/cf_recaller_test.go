package recall

import (
	"context"
	"testing"

	"sea/internal/cf"
	"sea/internal/domain"
)

// ============================================================================
// 该文件测试 CFRecaller 填充后的 Recall 逻辑，覆盖：
//   - Name() 返回 "cf"
//   - 冷启动路径（用户无历史行为 → ColdStartUser）
//   - 正常路径（三路 CF 并行召回 + 分数融合）
//   - 分数融合数学正确性（cf_fused = 0.4*cf_user + 0.3*cf_item + 0.3*cf_mf）
//   - 部分重叠候选的融合
//   - topK 截断
//   - req.TopK 覆盖默认 topK
//   - 编译期断言（位于 cf_recaller.go）
//
// 测试通过 mock 实现 UserCFEngine/ItemCFEngine/MFEngine interface 注入可控结果，
// 验证 CFRecaller 的融合逻辑而非 CF 算法本身（算法由并行 agent 的 cf 包测试）。
// ============================================================================

// makeCand 构造候选文章（含 Score）。
func makeCand(articleID string, score float64) domain.Candidate {
	return domain.Candidate{ArticleID: articleID, Score: score}
}

// --- mock CF 引擎实现 ---

// mockUserCFEngine UserCFEngine 的 mock 实现，按 userID 返回预设结果。
type mockUserCFEngine struct {
	results map[string][]domain.Candidate
}

func (m *mockUserCFEngine) Recommend(_ context.Context, req domain.RecallRequest, topK int) []domain.Candidate {
	cands := m.results[req.UserKey.UserID]
	if topK > 0 && len(cands) > topK {
		return cands[:topK]
	}
	return cands
}

// mockItemCFEngine ItemCFEngine 的 mock 实现。
type mockItemCFEngine struct {
	results map[string][]domain.Candidate
}

func (m *mockItemCFEngine) Recommend(_ context.Context, req domain.RecallRequest, topK int) []domain.Candidate {
	cands := m.results[req.UserKey.UserID]
	if topK > 0 && len(cands) > topK {
		return cands[:topK]
	}
	return cands
}

// mockMFEngine MFEngine 的 mock 实现。
type mockMFEngine struct {
	results map[string][]domain.Candidate
}

func (m *mockMFEngine) Recommend(_ context.Context, req domain.RecallRequest, topK int) []domain.Candidate {
	cands := m.results[req.UserKey.UserID]
	if topK > 0 && len(cands) > topK {
		return cands[:topK]
	}
	return cands
}

// --- mock HotArticleRepo（注意：fallback_test.go 已定义 mockHotRepo，此处用不同名称避免冲突）---

// mockHotRepoForCF 测试用 HotArticleRepo mock（cf_recaller_test 专用）。
type mockHotRepoForCF struct {
	cands []domain.Candidate
	err   error
}

func (m *mockHotRepoForCF) ListHot(_ context.Context, topK int) ([]domain.Candidate, error) {
	if m.err != nil {
		return nil, m.err
	}
	if topK > 0 && len(m.cands) > topK {
		return m.cands[:topK], nil
	}
	return m.cands, nil
}

// --- 测试用例 ---

// TestCFRecaller_Name 验证召回器名称。
func TestCFRecaller_Name(t *testing.T) {
	r := NewCFRecaller(nil, nil, nil, nil, 10)
	if got := r.Name(); got != "cf" {
		t.Errorf("Name() = %q, 期望 cf", got)
	}
}

// TestCFRecaller_ImplementsRecaller 验证 *CFRecaller 满足 domain.Recaller interface。
func TestCFRecaller_ImplementsRecaller(t *testing.T) {
	var r domain.Recaller = NewCFRecaller(nil, nil, nil, nil, 10)
	if r.Name() != "cf" {
		t.Errorf("通过 interface 调用 Name() = %q, 期望 cf", r.Name())
	}
}

// TestCFRecaller_ColdStart 验证冷启动路径：用户无历史行为时调 ColdStartUser。
func TestCFRecaller_ColdStart(t *testing.T) {
	// 构造 ColdStart（注入 mock repos）。
	hotRepo := &mockHotRepoForCF{
		cands: []domain.Candidate{
			makeCand("hot1", 0.9),
			makeCand("hot2", 0.8),
		},
	}
	cs := cf.NewColdStart(hotRepo, nil, 10)

	r := NewCFRecaller(nil, nil, nil, cs, 10)

	// 用户无历史行为（冷启动）。
	req := domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u_cold"},
		Profile: &domain.UserProfile{
			Static:   &domain.StaticProfile{Interests: []string{"科技"}},
			Behavior: &domain.BehaviorProfile{}, // 空行为
		},
	}

	res, err := r.Recall(context.Background(), req)
	if err != nil {
		t.Fatalf("Recall 失败: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("候选数 = %d, 期望 2", len(res.Candidates))
	}
	if res.Candidates[0].ArticleID != "hot1" {
		t.Errorf("首个候选 = %q, 期望 hot1", res.Candidates[0].ArticleID)
	}
	if res.Source != "cf" {
		t.Errorf("Source = %q, 期望 cf", res.Source)
	}
}

// TestCFRecaller_ColdStartNoHandler 验证冷启动但未注入 ColdStart 时返回错误。
func TestCFRecaller_ColdStartNoHandler(t *testing.T) {
	r := NewCFRecaller(nil, nil, nil, nil, 10)
	req := domain.RecallRequest{
		UserKey:  domain.UserKey{UserID: "u_cold"},
		Profile:  &domain.UserProfile{Behavior: &domain.BehaviorProfile{}},
	}
	_, err := r.Recall(context.Background(), req)
	if err == nil {
		t.Fatalf("期望冷启动无 handler 时返回错误")
	}
}

// TestCFRecaller_FuseScores 验证三路 CF 分数融合数学正确性。
func TestCFRecaller_FuseScores(t *testing.T) {
	userCF := &mockUserCFEngine{
		results: map[string][]domain.Candidate{
			"u1": {makeCand("art1", 1.0)},
		},
	}
	itemCF := &mockItemCFEngine{
		results: map[string][]domain.Candidate{
			"u1": {makeCand("art1", 0.5)},
		},
	}
	mf := &mockMFEngine{
		results: map[string][]domain.Candidate{
			"u1": {makeCand("art1", 0.5)},
		},
	}

	r := NewCFRecaller(userCF, itemCF, mf, nil, 10)

	req := domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		Profile: &domain.UserProfile{
			Behavior: &domain.BehaviorProfile{
				RecentClicks: []string{"old_art"}, // 有历史行为，非冷启动
			},
		},
	}

	res, err := r.Recall(context.Background(), req)
	if err != nil {
		t.Fatalf("Recall 失败: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("候选数 = %d, 期望 1", len(res.Candidates))
	}

	c := res.Candidates[0]
	if c.ArticleID != "art1" {
		t.Errorf("ArticleID = %q, 期望 art1", c.ArticleID)
	}

	// 验证四项分数写入。
	expectedFused := 0.4*1.0 + 0.3*0.5 + 0.3*0.5 // = 0.4 + 0.15 + 0.15 = 0.7
	if got := c.Scores["cf_user"]; !floatEq(got, 1.0) {
		t.Errorf("cf_user = %v, 期望 1.0", got)
	}
	if got := c.Scores["cf_item"]; !floatEq(got, 0.5) {
		t.Errorf("cf_item = %v, 期望 0.5", got)
	}
	if got := c.Scores["cf_mf"]; !floatEq(got, 0.5) {
		t.Errorf("cf_mf = %v, 期望 0.5", got)
	}
	if got := c.Scores["cf_fused"]; !floatEq(got, expectedFused) {
		t.Errorf("cf_fused = %v, 期望 %v", got, expectedFused)
	}
	if !floatEq(c.Score, expectedFused) {
		t.Errorf("Score = %v, 期望 %v", c.Score, expectedFused)
	}
}

// TestCFRecaller_FuseScoresPartialOverlap 验证部分重叠候选的融合。
// User-CF 返回 art1/art2；Item-CF 返回 art1/art3；MF 返回 art2/art3。
func TestCFRecaller_FuseScoresPartialOverlap(t *testing.T) {
	userCF := &mockUserCFEngine{
		results: map[string][]domain.Candidate{
			"u2": {
				makeCand("art1", 1.0),
				makeCand("art2", 0.8),
			},
		},
	}
	itemCF := &mockItemCFEngine{
		results: map[string][]domain.Candidate{
			"u2": {
				makeCand("art1", 0.6),
				makeCand("art3", 0.4),
			},
		},
	}
	mf := &mockMFEngine{
		results: map[string][]domain.Candidate{
			"u2": {
				makeCand("art2", 0.5),
				makeCand("art3", 0.3),
			},
		},
	}

	r := NewCFRecaller(userCF, itemCF, mf, nil, 10)

	req := domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u2"},
		Profile: &domain.UserProfile{
			Behavior: &domain.BehaviorProfile{
				RecentClicks: []string{"old"},
			},
		},
	}

	res, err := r.Recall(context.Background(), req)
	if err != nil {
		t.Fatalf("Recall 失败: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("候选数 = %d, 期望 3（art1/art2/art3）", len(res.Candidates))
	}

	// art1: 0.4*1.0 + 0.3*0.6 + 0.3*0 = 0.4 + 0.18 = 0.58
	// art2: 0.4*0.8 + 0.3*0 + 0.3*0.5 = 0.32 + 0.15 = 0.47
	// art3: 0.4*0 + 0.3*0.4 + 0.3*0.3 = 0.12 + 0.09 = 0.21
	// 排序应为 art1 > art2 > art3
	if res.Candidates[0].ArticleID != "art1" {
		t.Errorf("首位 = %q, 期望 art1", res.Candidates[0].ArticleID)
	}
	if res.Candidates[1].ArticleID != "art2" {
		t.Errorf("第二位 = %q, 期望 art2", res.Candidates[1].ArticleID)
	}
	if res.Candidates[2].ArticleID != "art3" {
		t.Errorf("第三位 = %q, 期望 art3", res.Candidates[2].ArticleID)
	}

	// 验证 art3 只出现在 Item-CF 和 MF，未出现在 User-CF。
	art3 := res.Candidates[2]
	if art3.Scores["cf_user"] != 0 {
		t.Errorf("art3 cf_user = %v, 期望 0", art3.Scores["cf_user"])
	}
}

// TestCFRecaller_TopKTruncation 验证 topK 截断。
func TestCFRecaller_TopKTruncation(t *testing.T) {
	userCF := &mockUserCFEngine{
		results: map[string][]domain.Candidate{
			"u3": {
				makeCand("a1", 0.9),
				makeCand("a2", 0.8),
				makeCand("a3", 0.7),
			},
		},
	}

	r := NewCFRecaller(userCF, nil, nil, nil, 2)

	req := domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u3"},
		Profile: &domain.UserProfile{
			Behavior: &domain.BehaviorProfile{RecentClicks: []string{"old"}},
		},
	}

	res, err := r.Recall(context.Background(), req)
	if err != nil {
		t.Fatalf("Recall 失败: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("候选数 = %d, 期望 2（topK 截断）", len(res.Candidates))
	}
}

// TestCFRecaller_ReqTopKOverride 验证 req.TopK 覆盖默认 topK。
func TestCFRecaller_ReqTopKOverride(t *testing.T) {
	userCF := &mockUserCFEngine{
		results: map[string][]domain.Candidate{
			"u4": {
				makeCand("a1", 0.9),
				makeCand("a2", 0.8),
				makeCand("a3", 0.7),
			},
		},
	}

	r := NewCFRecaller(userCF, nil, nil, nil, 10)

	req := domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u4"},
		Profile: &domain.UserProfile{
			Behavior: &domain.BehaviorProfile{RecentClicks: []string{"old"}},
		},
		TopK: 1,
	}

	res, err := r.Recall(context.Background(), req)
	if err != nil {
		t.Fatalf("Recall 失败: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("候选数 = %d, 期望 1（req.TopK 覆盖）", len(res.Candidates))
	}
}
