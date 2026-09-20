// ============================================================================
// 该文件定义三层 UserProfile 的辅助方法与构造函数。
// UserProfile / StaticProfile / DynamicProfile / BehaviorProfile / UserKey
// 的结构体定义见 types.go；本文件仅补充构造函数、校验与便捷访问方法。
//
// 三层画像职责：
//   - StaticProfile  静态层：注册时填写，缓慢变化（兴趣/用途/偏好/背景/难度等）
//   - DynamicProfile 动态层：行为累积（活跃度/会话数/累计阅读/偏好变化轨迹）
//   - BehaviorProfile 行为层：近期行为摘要（点击/点赞/收藏/完成阅读/搜索）
//
// 二开扩展点：可通过覆盖 NewUserProfile 默认值或实现 ProfileManager 自定义装配。
// ============================================================================

package domain

import "time"

// 默认保留近期行为条目数量。
const (
	// DefaultRecentBehaviorLimit 近期行为（点击/点赞/收藏/完成阅读/搜索）默认保留条数。
	DefaultRecentBehaviorLimit = 50
)

// NewUserKey 构造用户唯一标识。
// userID 用户 ID；channel 频道名（可为空表示默认流）。
func NewUserKey(userID, channel string) UserKey {
	return UserKey{UserID: userID, Channel: channel}
}

// String 返回 "userID:channel" 形式的稳定字符串，用于日志/缓存键。
func (k UserKey) String() string {
	return k.UserID + ":" + k.Channel
}

// IsEmpty 判断 UserKey 是否为零值（UserID 为空）。
func (k UserKey) IsEmpty() bool {
	return k.UserID == ""
}

// NewUserProfile 构造三层用户画像，初始化三层指针与更新时间。
// key 用户唯一标识；now 当前时间（可注入便于测试）。
func NewUserProfile(key UserKey, now time.Time) *UserProfile {
	return &UserProfile{
		Key:       key,
		Static:    &StaticProfile{RegisterAt: now},
		Dynamic:   &DynamicProfile{LastActiveAt: now},
		Behavior:  &BehaviorProfile{},
		UpdatedAt: now,
	}
}

// Touch 更新画像的 UpdatedAt 与动态层 LastActiveAt，用于行为回流时刷新活跃时间。
// now 当前时间。
func (p *UserProfile) Touch(now time.Time) {
	if p == nil {
		return
	}
	p.UpdatedAt = now
	if p.Dynamic != nil {
		p.Dynamic.LastActiveAt = now
	}
}

// IsActive 判断用户在给定窗口内是否活跃。
// now 当前时间；window 活跃窗口（如 30 分钟）。
func (p *UserProfile) IsActive(now time.Time, window time.Duration) bool {
	if p == nil || p.Dynamic == nil {
		return false
	}
	return now.Sub(p.Dynamic.LastActiveAt) <= window
}

// AppendRecentClick 追加最近点击文章并截断到 limit 条。
// articleID 文章 ID；limit 保留上限（<=0 时使用 DefaultRecentBehaviorLimit）。
func (b *BehaviorProfile) AppendRecentClick(articleID string, limit int) {
	if b == nil {
		return
	}
	if limit <= 0 {
		limit = DefaultRecentBehaviorLimit
	}
	b.RecentClicks = appendRecent(b.RecentClicks, articleID, limit)
}

// AppendRecentLike 追加最近点赞文章并截断到 limit 条。
func (b *BehaviorProfile) AppendRecentLike(articleID string, limit int) {
	if b == nil {
		return
	}
	if limit <= 0 {
		limit = DefaultRecentBehaviorLimit
	}
	b.RecentLikes = appendRecent(b.RecentLikes, articleID, limit)
}

// AppendRecentFavorite 追加最近收藏文章并截断到 limit 条。
func (b *BehaviorProfile) AppendRecentFavorite(articleID string, limit int) {
	if b == nil {
		return
	}
	if limit <= 0 {
		limit = DefaultRecentBehaviorLimit
	}
	b.RecentFavorites = appendRecent(b.RecentFavorites, articleID, limit)
}

// AppendRecentCompletion 追加最近完成阅读文章并截断到 limit 条。
func (b *BehaviorProfile) AppendRecentCompletion(articleID string, limit int) {
	if b == nil {
		return
	}
	if limit <= 0 {
		limit = DefaultRecentBehaviorLimit
	}
	b.RecentCompletions = appendRecent(b.RecentCompletions, articleID, limit)
}

// AppendRecentSearch 追加最近搜索关键词并截断到 limit 条。
func (b *BehaviorProfile) AppendRecentSearch(query string, limit int) {
	if b == nil {
		return
	}
	if limit <= 0 {
		limit = DefaultRecentBehaviorLimit
	}
	b.RecentSearches = appendRecent(b.RecentSearches, query, limit)
}

// RecordPreferenceChange 记录偏好变化轨迹到动态画像。
// change 偏好变化条目。
func (d *DynamicProfile) RecordPreferenceChange(change PreferenceChange) {
	if d == nil {
		return
	}
	d.PreferenceTrail = append(d.PreferenceTrail, change)
	// 轨迹同样截断到默认上限，避免无限增长。
	if len(d.PreferenceTrail) > DefaultRecentBehaviorLimit {
		d.PreferenceTrail = d.PreferenceTrail[len(d.PreferenceTrail)-DefaultRecentBehaviorLimit:]
	}
}

// appendRecent 内部工具：向 slice 追加元素并保留最近 limit 条。
func appendRecent(s []string, v string, limit int) []string {
	s = append(s, v)
	if len(s) > limit {
		s = s[len(s)-limit:]
	}
	return s
}
