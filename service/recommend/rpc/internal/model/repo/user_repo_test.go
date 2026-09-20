package repo

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 userRepoAdapter，覆盖：
//   - 正常：所有 store 成功，合并 UserProfile
//   - 部分失败：1-2 store 失败，PartialErrors 含错误但返回部分数据
//   - 全失败：所有 store 失败，返回 error
//   - PartialErrors.Error() 聚合信息
//   - PartialErrors errors.As 提取
//   - UpdateDynamic 写入
//   - UpdateTemporal 路由到 temporalStore
//   - LoadTemporal 从 temporalStore 读取
//   - 无 store 配置
//   - 无 temporalStore 时 UpdateTemporal/LoadTemporal 返回 error
// ============================================================================

// mockUserStore 模拟 UserStore。
type mockUserStore struct {
	name         string
	loadProfile  domain.UserProfile
	loadErr      error
	saveErr      error
	mu           sync.Mutex
	saveCount    int
	savedProfile domain.UserProfile
}

func (m *mockUserStore) Name() string { return m.name }

func (m *mockUserStore) Load(ctx context.Context, key domain.UserKey) (domain.UserProfile, error) {
	if m.loadErr != nil {
		return domain.UserProfile{}, m.loadErr
	}
	return m.loadProfile, nil
}

func (m *mockUserStore) Save(ctx context.Context, key domain.UserKey, profile domain.UserProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveCount++
	m.savedProfile = profile
	return m.saveErr
}

// mockTemporalStore 模拟实现 UserStore + temporalStore 扩展接口。
type mockTemporalStore struct {
	mockUserStore
	temporal      *domain.TemporalProfile
	loadTempErr   error
	saveTempErr   error
	mu2           sync.Mutex
	saveTempCount int
	savedTemporal domain.TemporalProfile
}

func (m *mockTemporalStore) LoadTemporal(ctx context.Context, key domain.UserKey) (*domain.TemporalProfile, error) {
	if m.loadTempErr != nil {
		return nil, m.loadTempErr
	}
	return m.temporal, nil
}

func (m *mockTemporalStore) SaveTemporal(ctx context.Context, key domain.UserKey, tp domain.TemporalProfile) error {
	m.mu2.Lock()
	defer m.mu2.Unlock()
	m.saveTempCount++
	m.savedTemporal = tp
	return m.saveTempErr
}

// TestLoad_AllSuccess 验证所有 store 成功时合并 UserProfile。
func TestLoad_AllSuccess(t *testing.T) {
	staticStore := &mockUserStore{
		name:        "static",
		loadProfile: domain.UserProfile{Static: &domain.StaticProfile{Tier: "vip"}},
	}
	dynamicStore := &mockUserStore{
		name:        "dynamic",
		loadProfile: domain.UserProfile{Dynamic: &domain.DynamicProfile{Topics: []string{"tech"}}},
	}
	behaviorStore := &mockUserStore{
		name: "behavior",
		loadProfile: domain.UserProfile{
			Behavior: &domain.BehaviorProfile{RecentClicks: []string{"a1", "a2"}},
		},
	}
	adapter := NewUserRepoAdapter(staticStore, dynamicStore, behaviorStore)

	profile, err := adapter.Load(context.Background(), domain.UserKey{UserID: "u1"})
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if profile.Static == nil || profile.Static.Tier != "vip" {
		t.Errorf("Static 合并失败: %+v", profile.Static)
	}
	if profile.Dynamic == nil || len(profile.Dynamic.Topics) != 1 || profile.Dynamic.Topics[0] != "tech" {
		t.Errorf("Dynamic 合并失败: %+v", profile.Dynamic)
	}
	if profile.Behavior == nil || len(profile.Behavior.RecentClicks) != 2 {
		t.Errorf("Behavior 合并失败: %+v", profile.Behavior)
	}
}

// TestLoad_PartialFailure 验证部分 store 失败时返回部分数据 + PartialErrors。
func TestLoad_PartialFailure(t *testing.T) {
	staticStore := &mockUserStore{
		name:        "static",
		loadProfile: domain.UserProfile{Static: &domain.StaticProfile{Tier: "vip"}},
	}
	dynamicStore := &mockUserStore{
		name:    "dynamic",
		loadErr: errors.New("dynamic store down"),
	}
	behaviorStore := &mockUserStore{
		name: "behavior",
		loadProfile: domain.UserProfile{
			Behavior: &domain.BehaviorProfile{RecentClicks: []string{"a1"}},
		},
	}
	adapter := NewUserRepoAdapter(staticStore, dynamicStore, behaviorStore)

	profile, err := adapter.Load(context.Background(), domain.UserKey{UserID: "u1"})
	if err == nil {
		t.Fatal("部分失败应返回 PartialErrors, 实际 nil")
	}
	// 验证返回的是 PartialErrors
	var pe *PartialErrors
	if !errors.As(err, &pe) {
		t.Fatalf("错误类型应为 *PartialErrors, 实际 %T", err)
	}
	if len(pe.Errors) != 1 {
		t.Errorf("PartialErrors 含 %d 个错误, 期望 1", len(pe.Errors))
	}
	// 验证仍返回部分数据
	if profile == nil {
		t.Fatal("部分失败应返回部分数据, 实际 nil")
	}
	if profile.Static == nil {
		t.Errorf("Static 应有数据（来自成功的 store）")
	}
	if profile.Behavior == nil {
		t.Errorf("Behavior 应有数据（来自成功的 store）")
	}
	// Dynamic 来自失败的 store，应为 nil
	if profile.Dynamic != nil {
		t.Errorf("Dynamic 应为 nil（store 失败）")
	}
}

// TestLoad_AllFailure 验证所有 store 失败时返回 error。
func TestLoad_AllFailure(t *testing.T) {
	s1 := &mockUserStore{name: "s1", loadErr: errors.New("s1 down")}
	s2 := &mockUserStore{name: "s2", loadErr: errors.New("s2 down")}
	adapter := NewUserRepoAdapter(s1, s2)

	profile, err := adapter.Load(context.Background(), domain.UserKey{UserID: "u1"})
	if err == nil {
		t.Fatal("全失败应返回 error, 实际 nil")
	}
	if profile != nil {
		t.Errorf("全失败应返回 nil profile, 实际 %+v", profile)
	}
	var pe *PartialErrors
	if !errors.As(err, &pe) {
		t.Fatalf("错误类型应为 *PartialErrors, 实际 %T", err)
	}
	if len(pe.Errors) != 2 {
		t.Errorf("PartialErrors 含 %d 个错误, 期望 2", len(pe.Errors))
	}
}

// TestLoad_NoStores 验证无 store 配置时返回 error。
func TestLoad_NoStores(t *testing.T) {
	adapter := NewUserRepoAdapter()
	_, err := adapter.Load(context.Background(), domain.UserKey{UserID: "u1"})
	if err == nil {
		t.Fatal("无 store 时应返回 error")
	}
}

// TestLoad_PartialErrorsContainsStoreName 验证 PartialErrors 包含 store 名称。
func TestLoad_PartialErrorsContainsStoreName(t *testing.T) {
	s1 := &mockUserStore{name: "static-store", loadErr: errors.New("connection refused")}
	adapter := NewUserRepoAdapter(s1)

	_, err := adapter.Load(context.Background(), domain.UserKey{UserID: "u1"})
	if err == nil {
		t.Fatal("应返回 error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "static-store") {
		t.Errorf("PartialErrors 应包含 store 名, 实际: %s", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("PartialErrors 应包含原始错误, 实际: %s", msg)
	}
}

// TestPartialErrors_Error 验证 PartialErrors.Error() 聚合信息。
func TestPartialErrors_Error(t *testing.T) {
	// nil 接收者
	var nilPE *PartialErrors
	if got := nilPE.Error(); got != "no errors" {
		t.Errorf("nil PartialErrors.Error() = %q, 期望 %q", got, "no errors")
	}
	// 空错误列表
	emptyPE := &PartialErrors{Errors: nil}
	if got := emptyPE.Error(); got != "no errors" {
		t.Errorf("空 PartialErrors.Error() = %q, 期望 %q", got, "no errors")
	}
	// 多错误
	pe := &PartialErrors{
		Errors: []error{
			errors.New("store A failed"),
			errors.New("store B failed"),
		},
	}
	msg := pe.Error()
	if !strings.Contains(msg, "2") {
		t.Errorf("Error() 应包含错误数 2, 实际: %s", msg)
	}
	if !strings.Contains(msg, "store A failed") || !strings.Contains(msg, "store B failed") {
		t.Errorf("Error() 应包含所有子错误, 实际: %s", msg)
	}
}

// TestPartialErrors_ErrorsAs 验证通过 errors.As 提取 PartialErrors。
func TestPartialErrors_ErrorsAs(t *testing.T) {
	s1 := &mockUserStore{name: "s1", loadErr: errors.New("down")}
	adapter := NewUserRepoAdapter(s1)

	_, err := adapter.Load(context.Background(), domain.UserKey{UserID: "u1"})

	var pe *PartialErrors
	if !errors.As(err, &pe) {
		t.Fatalf("errors.As 应能提取 *PartialErrors")
	}
	if len(pe.Errors) != 1 {
		t.Errorf("提取的 PartialErrors 含 %d 个错误, 期望 1", len(pe.Errors))
	}
}

// TestUpdateDynamic_Success 验证 UpdateDynamic 写入所有 store。
func TestUpdateDynamic_Success(t *testing.T) {
	s1 := &mockUserStore{name: "s1"}
	s2 := &mockUserStore{name: "s2"}
	adapter := NewUserRepoAdapter(s1, s2)

	dp := domain.DynamicProfile{Topics: []string{"tech"}, LastActiveAt: time.Now()}
	err := adapter.UpdateDynamic(context.Background(), domain.UserKey{UserID: "u1"}, dp)
	if err != nil {
		t.Fatalf("UpdateDynamic 返回错误: %v", err)
	}
	// 验证两个 store 都被调用 Save
	if s1.saveCount != 1 {
		t.Errorf("s1 Save 调用 %d 次, 期望 1", s1.saveCount)
	}
	if s2.saveCount != 1 {
		t.Errorf("s2 Save 调用 %d 次, 期望 1", s2.saveCount)
	}
	// 验证传入的 profile 含 Dynamic
	if s1.savedProfile.Dynamic == nil {
		t.Errorf("s1 保存的 profile 应含 Dynamic")
	}
	if s1.savedProfile.Dynamic.Topics[0] != "tech" {
		t.Errorf("s1 保存的 Dynamic.Topics[0] = %q, 期望 tech", s1.savedProfile.Dynamic.Topics[0])
	}
}

// TestUpdateDynamic_PartialFailure 验证 UpdateDynamic 部分失败返回 PartialErrors。
func TestUpdateDynamic_PartialFailure(t *testing.T) {
	s1 := &mockUserStore{name: "s1"}
	s2 := &mockUserStore{name: "s2", saveErr: errors.New("s2 save failed")}
	adapter := NewUserRepoAdapter(s1, s2)

	err := adapter.UpdateDynamic(context.Background(), domain.UserKey{UserID: "u1"}, domain.DynamicProfile{})
	if err == nil {
		t.Fatal("部分失败应返回 PartialErrors")
	}
	var pe *PartialErrors
	if !errors.As(err, &pe) {
		t.Fatalf("错误类型应为 *PartialErrors, 实际 %T", err)
	}
	if len(pe.Errors) != 1 {
		t.Errorf("PartialErrors 含 %d 个错误, 期望 1", len(pe.Errors))
	}
}

// TestUpdateTemporal_Success 验证 UpdateTemporal 路由到 temporalStore。
func TestUpdateTemporal_Success(t *testing.T) {
	regularStore := &mockUserStore{name: "regular"}
	tempStore := &mockTemporalStore{
		mockUserStore: mockUserStore{name: "temporal"},
	}
	adapter := NewUserRepoAdapter(regularStore, tempStore)

	tp := domain.TemporalProfile{
		LongTerm:  []domain.Interest{{Tag: "tech", Weight: 0.8}},
		ShortTerm: []domain.Interest{{Tag: "news", Weight: 0.5}},
	}
	err := adapter.UpdateTemporal(context.Background(), domain.UserKey{UserID: "u1"}, tp)
	if err != nil {
		t.Fatalf("UpdateTemporal 返回错误: %v", err)
	}
	// 验证 temporalStore 被调用 SaveTemporal
	if tempStore.saveTempCount != 1 {
		t.Errorf("temporalStore SaveTemporal 调用 %d 次, 期望 1", tempStore.saveTempCount)
	}
	// 验证 regular store 未被调用 SaveTemporal（它不实现 temporalStore）
	if regularStore.saveCount != 0 {
		t.Errorf("regular store 不应被调用 Save, 实际 %d 次", regularStore.saveCount)
	}
	// 验证保存的内容
	if len(tempStore.savedTemporal.LongTerm) != 1 {
		t.Errorf("保存的 LongTerm 长度 = %d, 期望 1", len(tempStore.savedTemporal.LongTerm))
	}
	if tempStore.savedTemporal.LongTerm[0].Tag != "tech" {
		t.Errorf("保存的 LongTerm[0].Tag = %q, 期望 tech", tempStore.savedTemporal.LongTerm[0].Tag)
	}
}

// TestUpdateTemporal_NoTemporalStore 验证无 temporalStore 时返回 error。
func TestUpdateTemporal_NoTemporalStore(t *testing.T) {
	s1 := &mockUserStore{name: "s1"}
	s2 := &mockUserStore{name: "s2"}
	adapter := NewUserRepoAdapter(s1, s2)

	err := adapter.UpdateTemporal(context.Background(), domain.UserKey{UserID: "u1"}, domain.TemporalProfile{})
	if err == nil {
		t.Fatal("无 temporalStore 时应返回 error")
	}
}

// TestUpdateTemporal_PartialFailure 验证 UpdateTemporal 部分失败返回 PartialErrors。
func TestUpdateTemporal_PartialFailure(t *testing.T) {
	ts1 := &mockTemporalStore{
		mockUserStore: mockUserStore{name: "ts1"},
	}
	ts2 := &mockTemporalStore{
		mockUserStore: mockUserStore{name: "ts2"},
		saveTempErr:   errors.New("ts2 save failed"),
	}
	adapter := NewUserRepoAdapter(ts1, ts2)

	err := adapter.UpdateTemporal(context.Background(), domain.UserKey{UserID: "u1"}, domain.TemporalProfile{})
	if err == nil {
		t.Fatal("部分失败应返回 PartialErrors")
	}
	var pe *PartialErrors
	if !errors.As(err, &pe) {
		t.Fatalf("错误类型应为 *PartialErrors, 实际 %T", err)
	}
	if len(pe.Errors) != 1 {
		t.Errorf("PartialErrors 含 %d 个错误, 期望 1", len(pe.Errors))
	}
}

// TestLoadTemporal_Success 验证 LoadTemporal 从 temporalStore 读取。
func TestLoadTemporal_Success(t *testing.T) {
	expected := &domain.TemporalProfile{
		LongTerm: []domain.Interest{{Tag: "tech", Weight: 0.8}},
	}
	tempStore := &mockTemporalStore{
		mockUserStore: mockUserStore{name: "temporal"},
		temporal:      expected,
	}
	regularStore := &mockUserStore{name: "regular"}
	adapter := NewUserRepoAdapter(regularStore, tempStore)

	got, err := adapter.LoadTemporal(context.Background(), domain.UserKey{UserID: "u1"})
	if err != nil {
		t.Fatalf("LoadTemporal 返回错误: %v", err)
	}
	if got == nil || len(got.LongTerm) != 1 || got.LongTerm[0].Tag != "tech" {
		t.Errorf("LoadTemporal 返回 %+v, 期望 LongTerm 含 tech", got)
	}
}

// TestLoadTemporal_NoTemporalStore 验证无 temporalStore 时返回 error。
func TestLoadTemporal_NoTemporalStore(t *testing.T) {
	s1 := &mockUserStore{name: "s1"}
	adapter := NewUserRepoAdapter(s1)

	_, err := adapter.LoadTemporal(context.Background(), domain.UserKey{UserID: "u1"})
	if err == nil {
		t.Fatal("无 temporalStore 时应返回 error")
	}
}

// TestLoadTemporal_AllFail 验证所有 temporalStore 失败时返回 PartialErrors。
func TestLoadTemporal_AllFail(t *testing.T) {
	ts1 := &mockTemporalStore{
		mockUserStore: mockUserStore{name: "ts1"},
		loadTempErr:   errors.New("ts1 down"),
	}
	ts2 := &mockTemporalStore{
		mockUserStore: mockUserStore{name: "ts2"},
		loadTempErr:   errors.New("ts2 down"),
	}
	adapter := NewUserRepoAdapter(ts1, ts2)

	_, err := adapter.LoadTemporal(context.Background(), domain.UserKey{UserID: "u1"})
	if err == nil {
		t.Fatal("全失败应返回 error")
	}
	var pe *PartialErrors
	if !errors.As(err, &pe) {
		t.Fatalf("错误类型应为 *PartialErrors, 实际 %T", err)
	}
	if len(pe.Errors) != 2 {
		t.Errorf("PartialErrors 含 %d 个错误, 期望 2", len(pe.Errors))
	}
}

// TestSaveAll_NoStores 验证 saveAll 无 store 时返回 error。
func TestSaveAll_NoStores(t *testing.T) {
	adapter := NewUserRepoAdapter()
	err := adapter.UpdateDynamic(context.Background(), domain.UserKey{UserID: "u1"}, domain.DynamicProfile{})
	if err == nil {
		t.Fatal("无 store 时应返回 error")
	}
}

// TestLoad_PartialFailureTwoStores 验证 2 个 store 失败时 PartialErrors 含 2 个错误。
func TestLoad_PartialFailureTwoStores(t *testing.T) {
	s1 := &mockUserStore{name: "s1", loadProfile: domain.UserProfile{Static: &domain.StaticProfile{}}}
	s2 := &mockUserStore{name: "s2", loadErr: errors.New("s2 down")}
	s3 := &mockUserStore{name: "s3", loadErr: errors.New("s3 down")}
	s4 := &mockUserStore{name: "s4", loadProfile: domain.UserProfile{Behavior: &domain.BehaviorProfile{}}}
	adapter := NewUserRepoAdapter(s1, s2, s3, s4)

	profile, err := adapter.Load(context.Background(), domain.UserKey{UserID: "u1"})
	if err == nil {
		t.Fatal("部分失败应返回 PartialErrors")
	}
	var pe *PartialErrors
	if !errors.As(err, &pe) {
		t.Fatalf("错误类型应为 *PartialErrors, 实际 %T", err)
	}
	if len(pe.Errors) != 2 {
		t.Errorf("PartialErrors 含 %d 个错误, 期望 2", len(pe.Errors))
	}
	if profile == nil {
		t.Fatal("部分失败应返回部分数据")
	}
	if profile.Static == nil {
		t.Errorf("Static 应有数据")
	}
	if profile.Behavior == nil {
		t.Errorf("Behavior 应有数据")
	}
}
