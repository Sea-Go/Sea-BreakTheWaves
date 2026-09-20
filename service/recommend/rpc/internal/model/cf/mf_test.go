package cf

import (
	"math"
	"testing"
)

func TestMFTrainConvergence(t *testing.T) {
	mf := NewMF(8, 0.05, 0.01)
	interactions := []Interaction{
		{"u1", "a1", 5},
		{"u1", "a2", 3},
		{"u2", "a1", 4},
		{"u2", "a3", 2},
		{"u3", "a2", 5},
		{"u3", "a3", 1},
	}
	// 训练前 RMSE（因子未初始化，预测全为 0）
	before := mf.RMSE(interactions)
	if err := mf.Train(interactions, 100); err != nil {
		t.Fatalf("train failed: %v", err)
	}
	after := mf.RMSE(interactions)
	// 训练后 RMSE 必须下降
	if after >= before {
		t.Fatalf("RMSE should decrease after training: before=%v after=%v", before, after)
	}
	// 应显著下降（过拟合小数据集）
	if after > before*0.5 {
		t.Fatalf("RMSE should decrease significantly: before=%v after=%v", before, after)
	}
}

func TestMFPredict(t *testing.T) {
	mf := NewMF(8, 0.05, 0.01)
	interactions := []Interaction{
		{"u1", "a1", 5},
		{"u2", "a1", 3},
	}
	if err := mf.Train(interactions, 50); err != nil {
		t.Fatalf("train failed: %v", err)
	}
	// 已知用户/文章应有有限预测值
	pred := mf.Predict("u1", "a1")
	if math.IsNaN(pred) || math.IsInf(pred, 0) {
		t.Fatalf("predict should be finite, got %v", pred)
	}
	// 未知用户返回 0（冷启动）
	if got := mf.Predict("unknown", "a1"); got != 0 {
		t.Fatalf("unknown user predict should be 0, got %v", got)
	}
	// 未知文章返回 0（冷启动）
	if got := mf.Predict("u1", "unknown"); got != 0 {
		t.Fatalf("unknown item predict should be 0, got %v", got)
	}
}

func TestMFRMSE(t *testing.T) {
	mf := NewMF(8, 0.05, 0.01)
	interactions := []Interaction{
		{"u1", "a1", 5},
		{"u1", "a2", 3},
	}
	// 未训练：RMSE 应为正（预测全 0，误差即评分本身）
	rmse := mf.RMSE(interactions)
	if rmse <= 0 {
		t.Fatalf("untrained RMSE should be positive, got %v", rmse)
	}
	// 空集返回 0
	if got := mf.RMSE(nil); got != 0 {
		t.Fatalf("empty RMSE should be 0, got %v", got)
	}
}

func TestMFRecommend(t *testing.T) {
	mf := NewMF(8, 0.05, 0.01)
	interactions := []Interaction{
		{"u1", "a1", 5},
		{"u1", "a2", 3},
		{"u2", "a1", 4},
		{"u2", "a3", 2},
	}
	if err := mf.Train(interactions, 50); err != nil {
		t.Fatalf("train failed: %v", err)
	}
	cands := mf.Recommend("u1", []string{"a1", "a2", "a3"}, 2)
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	// 应按预测分降序排列
	if cands[0].Score < cands[1].Score {
		t.Fatalf("candidates should be sorted desc by score")
	}
	if cands[0].Source != "cf" {
		t.Fatalf("expected source cf, got %s", cands[0].Source)
	}
}

func TestMFTrainErrors(t *testing.T) {
	mf := NewMF(8, 0.05, 0.01)
	// 空交互集应报错
	if err := mf.Train(nil, 10); err == nil {
		t.Fatal("expected error for empty interactions")
	}
	// 零 epoch 应报错
	if err := mf.Train([]Interaction{{"u1", "a1", 1}}, 0); err == nil {
		t.Fatal("expected error for zero epochs")
	}
}
