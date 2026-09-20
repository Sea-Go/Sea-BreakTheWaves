package recall

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
)

// ============================================================================
// 该文件使用 mock ChannelPoolRepo 测试 ChannelRecaller，覆盖：
//   - 频道分桶：根据 req.Channel 取该频道独立召回池
//   - topK 截断（req.TopK 与 recaller 默认 topK 优先级）
//   - Source 标记为 "channel"
//   - 错误传播
//   - Name() 返回 "channel"
//   - 编译期断言 *ChannelRecaller 实现 domain.Recaller
// ============================================================================

// 编译期断言：*ChannelRecaller 实现 domain.Recaller interface。
var _ domain.Recaller = (*ChannelRecaller)(nil)

// mockChannelPoolRepo 模拟 ChannelPoolRepo，记录调用参数并返回固定结果。
type mockChannelPoolRepo struct {
	fn func(ctx context.Context, channel string, topK int) ([]domain.Candidate, error)

	// 调用记录
	lastChannel string
	lastTopK    int
	calls       int
}

func (m *mockChannelPoolRepo) GetChannelPool(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
	m.calls++
	m.lastChannel = channel
	m.lastTopK = topK
	if m.fn != nil {
		return m.fn(ctx, channel, topK)
	}
	return nil, nil
}

// TestChannelRecaller_Name 验证召回器名称。
func TestChannelRecaller_Name(t *testing.T) {
	r := NewChannelRecaller(&mockChannelPoolRepo{}, 10)
	if got := r.Name(); got != "channel" {
		t.Errorf("Name() = %q, 期望 channel", got)
	}
}

// TestChannelRecaller_Recall_ChannelBucketing 验证根据 req.Channel 取该频道独立召回池。
func TestChannelRecaller_Recall_ChannelBucketing(t *testing.T) {
	mock := &mockChannelPoolRepo{
		fn: func(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
			if channel != "tech" {
				t.Errorf("channel = %q, 期望 tech", channel)
			}
			if topK != 5 {
				t.Errorf("topK = %d, 期望 5", topK)
			}
			return []domain.Candidate{
				{ArticleID: "a1", Score: 0.9, Source: "channel"},
				{ArticleID: "a2", Score: 0.8, Source: "channel"},
			}, nil
		},
	}
	r := NewChannelRecaller(mock, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		Channel: "tech",
		TopK:    5,
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Source != "channel" {
		t.Errorf("Source = %q, 期望 channel", res.Source)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("候选数 = %d, 期望 2", len(res.Candidates))
	}
	if mock.calls != 1 {
		t.Errorf("GetChannelPool 调用次数 = %d, 期望 1", mock.calls)
	}
	if mock.lastChannel != "tech" {
		t.Errorf("lastChannel = %q, 期望 tech", mock.lastChannel)
	}
}

// TestChannelRecaller_Recall_DefaultTopKFromRecaller 验证 req.TopK<=0 时使用 recaller 默认 topK。
func TestChannelRecaller_Recall_DefaultTopKFromRecaller(t *testing.T) {
	var capturedTopK int
	mock := &mockChannelPoolRepo{
		fn: func(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
			capturedTopK = topK
			return nil, nil
		},
	}
	r := NewChannelRecaller(mock, 7)
	_, _ = r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		Channel: "tech",
		// TopK 为 0，应使用 recaller 默认 7
	})
	if capturedTopK != 7 {
		t.Errorf("TopK = %d, 期望回退到 recaller 默认 7", capturedTopK)
	}
}

// TestChannelRecaller_Recall_SourceBackfill 验证仓储返回的候选若未设 Source 则补 "channel"。
func TestChannelRecaller_Recall_SourceBackfill(t *testing.T) {
	mock := &mockChannelPoolRepo{
		fn: func(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
			return []domain.Candidate{
				{ArticleID: "a1", Score: 0.9}, // Source 为空
				{ArticleID: "a2", Score: 0.8, Source: "precomputed"}, // Source 已设
			}, nil
		},
	}
	r := NewChannelRecaller(mock, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		Channel: "tech",
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Candidates[0].Source != "channel" {
		t.Errorf("候选 a1 Source = %q, 期望 channel", res.Candidates[0].Source)
	}
	if res.Candidates[1].Source != "precomputed" {
		t.Errorf("候选 a2 Source = %q, 期望保留 precomputed", res.Candidates[1].Source)
	}
}

// TestChannelRecaller_Recall_TopKCap 验证召回结果超过 topK 时被截断。
func TestChannelRecaller_Recall_TopKCap(t *testing.T) {
	mock := &mockChannelPoolRepo{
		fn: func(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
			cands := make([]domain.Candidate, 0, 8)
			for i := 0; i < 8; i++ {
				cands = append(cands, domain.Candidate{ArticleID: "a", Source: "channel"})
			}
			return cands, nil
		},
	}
	r := NewChannelRecaller(mock, 3)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		Channel: "tech",
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Errorf("候选数 = %d, 期望被截断到 3", len(res.Candidates))
	}
}

// TestChannelRecaller_Recall_EmptyChannel 验证空频道名时仍正常调用仓储（不报错）。
func TestChannelRecaller_Recall_EmptyChannel(t *testing.T) {
	var capturedChannel string
	mock := &mockChannelPoolRepo{
		fn: func(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
			capturedChannel = channel
			return []domain.Candidate{{ArticleID: "a1", Source: "channel"}}, nil
		},
	}
	r := NewChannelRecaller(mock, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		// Channel 为空（默认流）
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if capturedChannel != "" {
		t.Errorf("期望透传空频道名, 实际 %q", capturedChannel)
	}
	if len(res.Candidates) != 1 {
		t.Errorf("候选数 = %d, 期望 1", len(res.Candidates))
	}
}

// TestChannelRecaller_Recall_ErrorPropagation 验证 ChannelPoolRepo 错误向上传播。
func TestChannelRecaller_Recall_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("channel pool down")
	mock := &mockChannelPoolRepo{
		fn: func(ctx context.Context, channel string, topK int) ([]domain.Candidate, error) {
			return nil, sentinel
		},
	}
	r := NewChannelRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		Channel: "tech",
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("期望错误 %v 传播, 实际 %v", sentinel, err)
	}
}
