package profile

import (
	"math"
	"testing"
	"time"

	"sea/internal/domain"
)

// ============================================================================
// 该文件测试趋势检测（Trending Detection），覆盖：
//   - DetectTrending 检测 2σ 突增兴趣
//   - DetectTrending 无突增时返回空
//   - DetectTrending 空输入返回 nil
//   - DetectTrending 多个突增按 Weight 降序返回
//   - DetectTrending long_std=0 退化判定（所有长期权重相同）
//   - mean / std 辅助函数正确性
// ============================================================================

// TestDetectTrending_Spike 验证 2σ 突增检测。
//
// LongTerm 基线：[a:0.1, b:0.2, c:0.15] → mean≈0.15, std≈0.0408
// ShortTerm：[a:0.1, b:0.5, c:0.15]
//   - b: z=(0.5-0.15)/0.0408≈8.58 > 2 → Trending
//   - a: z=(0.1-0.15)/0.0408≈-1.22 < 2 → 非 Trending
//   - c: z=(0.15-0.15)/0.0408=0 < 2 → 非 Trending
func TestDetectTrending_Spike(t *testing.T) {
	longTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.1},
		{Tag: "b", Weight: 0.2},
		{Tag: "c", Weight: 0.15},
	}
	shortTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.1},
		{Tag: "b", Weight: 0.5}, // 突增
		{Tag: "c", Weight: 0.15},
	}
	now := time.Now()
	trending := DetectTrending(shortTerm, longTerm, now)

	if len(trending) != 1 {
		t.Fatalf("突增兴趣数 = %d, 期望 1", len(trending))
	}
	if trending[0].Tag != "b" {
		t.Errorf("突增 Tag = %q, 期望 b", trending[0].Tag)
	}
	if trending[0].Weight != 0.5 {
		t.Errorf("突增 Weight = %v, 期望 0.5", trending[0].Weight)
	}
	// 验证时间戳更新
	if !trending[0].LastReinforcedAt.Equal(now) {
		t.Errorf("LastReinforcedAt = %v, 期望 %v", trending[0].LastReinforcedAt, now)
	}
}

// TestDetectTrending_NoSpike 验证无突增时返回空。
//
// LongTerm：[a:0.1, b:0.2, c:0.3] → mean=0.2, std≈0.0816
// ShortTerm：[a:0.1, b:0.22, c:0.3]
//   - a: z≈-1.22 < 2 → 非 Trending
//   - b: z≈0.245 < 2 → 非 Trending
//   - c: z≈1.22 < 2 → 非 Trending
func TestDetectTrending_NoSpike(t *testing.T) {
	longTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.1},
		{Tag: "b", Weight: 0.2},
		{Tag: "c", Weight: 0.3},
	}
	shortTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.1},
		{Tag: "b", Weight: 0.22},
		{Tag: "c", Weight: 0.3},
	}
	trending := DetectTrending(shortTerm, longTerm, time.Now())
	if len(trending) != 0 {
		t.Errorf("无突增时应返回空, 实际 %d 个", len(trending))
	}
}

// TestDetectTrending_EmptyShortTerm 验证空 ShortTerm 返回 nil。
func TestDetectTrending_EmptyShortTerm(t *testing.T) {
	longTerm := []domain.InterestEntry{{Tag: "a", Weight: 0.5}}
	if got := DetectTrending(nil, longTerm, time.Now()); got != nil {
		t.Errorf("空 ShortTerm 应返回 nil, 实际 %v", got)
	}
	if got := DetectTrending([]domain.InterestEntry{}, longTerm, time.Now()); got != nil {
		t.Errorf("空 ShortTerm 应返回 nil, 实际 %v", got)
	}
}

// TestDetectTrending_MultipleSpikes 验证多个突增按 Weight 降序返回。
func TestDetectTrending_MultipleSpikes(t *testing.T) {
	longTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.1},
		{Tag: "b", Weight: 0.2},
		{Tag: "c", Weight: 0.15},
		{Tag: "d", Weight: 0.12},
	}
	// b 和 d 都突增
	shortTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.1},
		{Tag: "b", Weight: 0.9}, // 突增
		{Tag: "c", Weight: 0.15},
		{Tag: "d", Weight: 0.7}, // 突增
	}
	trending := DetectTrending(shortTerm, longTerm, time.Now())
	if len(trending) != 2 {
		t.Fatalf("突增兴趣数 = %d, 期望 2", len(trending))
	}
	// 降序：b(0.9) > d(0.7)
	if trending[0].Tag != "b" {
		t.Errorf("首位 Tag = %q, 期望 b（Weight=0.9）", trending[0].Tag)
	}
	if trending[1].Tag != "d" {
		t.Errorf("第二位 Tag = %q, 期望 d（Weight=0.7）", trending[1].Tag)
	}
}

// TestDetectTrending_StdZeroDegeneration 验证 long_std=0 退化判定。
//
// LongTerm 所有权重相同（std=0），退化为绝对差判定：
// short_weight > long_mean 即视为突增。
func TestDetectTrending_StdZeroDegeneration(t *testing.T) {
	longTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.3},
		{Tag: "b", Weight: 0.3},
		{Tag: "c", Weight: 0.3},
	}
	shortTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.3},  // 不突增（等于均值）
		{Tag: "b", Weight: 0.5},  // 突增（大于均值）
		{Tag: "c", Weight: 0.29}, // 不突增（小于均值）
	}
	trending := DetectTrending(shortTerm, longTerm, time.Now())
	if len(trending) != 1 {
		t.Fatalf("突增兴趣数 = %d, 期望 1（std=0 退化判定）", len(trending))
	}
	if trending[0].Tag != "b" {
		t.Errorf("突增 Tag = %q, 期望 b", trending[0].Tag)
	}
}

// TestDetectTrending_EmptyLongTerm 验证空 LongTerm 基线时所有非零短期兴趣为突增。
func TestDetectTrending_EmptyLongTerm(t *testing.T) {
	shortTerm := []domain.InterestEntry{
		{Tag: "a", Weight: 0.5},
		{Tag: "b", Weight: 0.0}, // 权重 0，不突增
	}
	trending := DetectTrending(shortTerm, nil, time.Now())
	if len(trending) != 1 {
		t.Fatalf("突增兴趣数 = %d, 期望 1（空基线时非零即突增）", len(trending))
	}
	if trending[0].Tag != "a" {
		t.Errorf("突增 Tag = %q, 期望 a", trending[0].Tag)
	}
}

// TestMean 验证 mean 函数。
func TestMean(t *testing.T) {
	cases := []struct {
		name string
		xs   []float64
		want float64
	}{
		{"empty", nil, 0},
		{"single", []float64{0.5}, 0.5},
		{"multi", []float64{0.1, 0.2, 0.3}, 0.2},
		{"zeros", []float64{0, 0, 0}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mean(c.xs); math.Abs(got-c.want) > 1e-9 {
				t.Errorf("mean(%v) = %v, 期望 %v", c.xs, got, c.want)
			}
		})
	}
}

// TestStd 验证 std 函数。
func TestStd(t *testing.T) {
	cases := []struct {
		name string
		xs   []float64
		m    float64
		want float64
	}{
		{"empty", nil, 0, 0},
		{"single", []float64{0.5}, 0.5, 0},
		{"same_values", []float64{0.3, 0.3, 0.3}, 0.3, 0},
		{"varied", []float64{0.1, 0.2, 0.3}, 0.2, math.Sqrt((0.01 + 0 + 0.01) / 3)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := std(c.xs, c.m); math.Abs(got-c.want) > 1e-9 {
				t.Errorf("std(%v, m=%v) = %v, 期望 %v", c.xs, c.m, got, c.want)
			}
		})
	}
}
