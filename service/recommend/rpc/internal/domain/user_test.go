// ============================================================================
// 该文件测试三层 UserProfile 的类型完整性与辅助方法行为。
// 覆盖：类型编译、字段完整性、构造函数、辅助方法（Touch/IsActive/AppendRecent*）。
// ============================================================================

package domain

import (
	"testing"
	"time"
)

// TestUserProfile_TypeCompleteness 验证三层画像结构体字段完整性（编译期保证）。
func TestUserProfile_TypeCompleteness(t *testing.T) {
	// 静态层：注册时填写的全部字段。
	s := StaticProfile{
		Tier:                  "vip",
		RegisterAt:            time.Now(),
		Tags:                  []string{"tech"},
		Interests:             []string{"AI", "旅行"},
		Purpose:               "学习",
		PreferredArticleTypes: []string{"深度长文"},
		PreferredLength:       "long",
		PreferredStyle:        "严肃",
		Background:            "工程师",
		Difficulty:            "expert",
		ExcludedContent:       []string{"娱乐"},
		ReadingPeriods:        []string{"09-12"},
		PersonalizationType:   "balanced",
	}
	if s.Purpose != "学习" {
		t.Fatalf("StaticProfile.Purpose = %q, want %q", s.Purpose, "学习")
	}

	// 动态层：行为累积字段。
	d := DynamicProfile{
		Topics:          []string{"AI"},
		LastActiveAt:    time.Now(),
		Activity:        0.8,
		SessionCount:    10,
		TotalReads:      100,
		PreferenceTrail: []PreferenceChange{{Tag: "AI", Action: "add", Delta: 0.5, ChangedAt: time.Now()}},
	}
	if d.Activity != 0.8 {
		t.Fatalf("DynamicProfile.Activity = %v, want 0.8", d.Activity)
	}

	// 行为层：近期行为摘要字段。
	b := BehaviorProfile{
		RecentClicks:      []string{"a1"},
		LikedTags:         map[string]int{"AI": 3},
		DislikedTags:      map[string]int{"娱乐": 1},
		RecentLikes:       []string{"a1"},
		RecentFavorites:   []string{"a2"},
		RecentCompletions: []string{"a3"},
		RecentSearches:    []string{"AI"},
	}
	if len(b.RecentFavorites) != 1 {
		t.Fatalf("BehaviorProfile.RecentFavorites len = %d, want 1", len(b.RecentFavorites))
	}

	// 三层画像：Key + 三层指针 + UpdatedAt。
	now := time.Now()
	p := UserProfile{
		Key:       UserKey{UserID: "u1", Channel: "tech"},
		Static:    &s,
		Dynamic:   &d,
		Behavior:  &b,
		UpdatedAt: now,
	}
	if p.Key.UserID != "u1" {
		t.Fatalf("UserProfile.Key.UserID = %q, want u1", p.Key.UserID)
	}
}

// TestUserKey_StringAndIsEmpty 验证 UserKey 的 String 与 IsEmpty 方法。
func TestUserKey_StringAndIsEmpty(t *testing.T) {
	k := NewUserKey("u1", "tech")
	if got := k.String(); got != "u1:tech" {
		t.Fatalf("UserKey.String() = %q, want %q", got, "u1:tech")
	}
	if k.IsEmpty() {
		t.Fatalf("UserKey.IsEmpty() = true, want false")
	}

	var zero UserKey
	if !zero.IsEmpty() {
		t.Fatalf("zero UserKey.IsEmpty() = false, want true")
	}
}

// TestNewUserProfile 验证构造函数初始化三层指针。
func TestNewUserProfile(t *testing.T) {
	now := time.Now()
	p := NewUserProfile(UserKey{UserID: "u1", Channel: "tech"}, now)
	if p == nil || p.Static == nil || p.Dynamic == nil || p.Behavior == nil {
		t.Fatalf("NewUserProfile 未正确初始化三层指针")
	}
	if !p.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt = %v, want %v", p.UpdatedAt, now)
	}
	if !p.Static.RegisterAt.Equal(now) {
		t.Fatalf("Static.RegisterAt = %v, want %v", p.Static.RegisterAt, now)
	}
}

// TestUserProfile_Touch 验证 Touch 刷新 UpdatedAt 与 LastActiveAt。
func TestUserProfile_Touch(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	p := NewUserProfile(UserKey{UserID: "u1"}, old)
	now := time.Now()
	p.Touch(now)
	if !p.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt = %v, want %v", p.UpdatedAt, now)
	}
	if !p.Dynamic.LastActiveAt.Equal(now) {
		t.Fatalf("Dynamic.LastActiveAt = %v, want %v", p.Dynamic.LastActiveAt, now)
	}
}

// TestUserProfile_IsActive 验证活跃窗口判定。
func TestUserProfile_IsActive(t *testing.T) {
	now := time.Now()
	p := NewUserProfile(UserKey{UserID: "u1"}, now.Add(-10*time.Minute))
	// 10 分钟前活跃，窗口 30 分钟 → 活跃。
	if !p.IsActive(now, 30*time.Minute) {
		t.Fatalf("IsActive(30min) = false, want true")
	}
	// 10 分钟前活跃，窗口 5 分钟 → 不活跃。
	if p.IsActive(now, 5*time.Minute) {
		t.Fatalf("IsActive(5min) = true, want false")
	}
}

// TestBehaviorProfile_AppendRecent 验证近期行为追加与截断。
func TestBehaviorProfile_AppendRecent(t *testing.T) {
	b := &BehaviorProfile{}
	// 追加 3 条点击，limit=2 → 仅保留最近 2 条。
	b.AppendRecentClick("a1", 2)
	b.AppendRecentClick("a2", 2)
	b.AppendRecentClick("a3", 2)
	if len(b.RecentClicks) != 2 {
		t.Fatalf("RecentClicks len = %d, want 2", len(b.RecentClicks))
	}
	if b.RecentClicks[0] != "a2" || b.RecentClicks[1] != "a3" {
		t.Fatalf("RecentClicks = %v, want [a2 a3]", b.RecentClicks)
	}

	// limit<=0 使用默认上限。
	b.AppendRecentLike("a1", 0)
	if len(b.RecentLikes) != 1 {
		t.Fatalf("RecentLikes len = %d, want 1", len(b.RecentLikes))
	}

	// 验证其它追加方法。
	b.AppendRecentFavorite("a1", 5)
	b.AppendRecentCompletion("a1", 5)
	b.AppendRecentSearch("AI", 5)
	if len(b.RecentFavorites) != 1 || len(b.RecentCompletions) != 1 || len(b.RecentSearches) != 1 {
		t.Fatalf("Favorites/Completions/Searches 未正确追加")
	}
}

// TestDynamicProfile_RecordPreferenceChange 验证偏好轨迹记录与截断。
func TestDynamicProfile_RecordPreferenceChange(t *testing.T) {
	d := &DynamicProfile{}
	// 追加超过默认上限的轨迹，验证截断。
	for i := 0; i < DefaultRecentBehaviorLimit+5; i++ {
		d.RecordPreferenceChange(PreferenceChange{
			Tag:       "AI",
			Action:    "add",
			Delta:     0.1,
			ChangedAt: time.Now(),
		})
	}
	if len(d.PreferenceTrail) != DefaultRecentBehaviorLimit {
		t.Fatalf("PreferenceTrail len = %d, want %d", len(d.PreferenceTrail), DefaultRecentBehaviorLimit)
	}
}
