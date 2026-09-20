package session

import (
	"context"
	"testing"
)

// ============================================================================
// 该文件测试 ChannelFilterKey / ChannelFilter 的 Set/Get/Isolate 与频道切换隔离。
// 覆盖：
//   - ChannelFilterKey.String() 格式
//   - NewChannelFilterKey 构造
//   - Set/Get 设置与获取用户当前频道
//   - Get 未设置用户返回 false
//   - Isolate 注入 context + FilterKeyFromContext 提取
//   - 频道切换后 filterKey 更新（隔离）
// ============================================================================

// TestChannelFilterKey_String 验证 String() 返回 {userID}:{channel} 格式。
func TestChannelFilterKey_String(t *testing.T) {
	k := NewChannelFilterKey("u1", "ch1")
	if got := k.String(); got != "u1:ch1" {
		t.Errorf("String() = %q, 期望 u1:ch1", got)
	}
}

// TestChannelFilterKey_Empty 验证空字段时 String() 不 panic。
func TestChannelFilterKey_Empty(t *testing.T) {
	k := NewChannelFilterKey("", "")
	if got := k.String(); got != ":" {
		t.Errorf("String() = %q, 期望 :", got)
	}
}

// TestChannelFilter_SetGet 验证 Set 后 Get 返回正确频道。
func TestChannelFilter_SetGet(t *testing.T) {
	f := NewChannelFilter()
	f.Set("u1", "ch1")

	k, ok := f.Get("u1")
	if !ok {
		t.Fatalf("Get(u1) 返回 false, 期望 true")
	}
	if k.UserID != "u1" {
		t.Errorf("UserID = %q, 期望 u1", k.UserID)
	}
	if k.Channel != "ch1" {
		t.Errorf("Channel = %q, 期望 ch1", k.Channel)
	}
	if got := k.String(); got != "u1:ch1" {
		t.Errorf("String() = %q, 期望 u1:ch1", got)
	}
}

// TestChannelFilter_GetNotFound 验证未设置用户 Get 返回 false。
func TestChannelFilter_GetNotFound(t *testing.T) {
	f := NewChannelFilter()
	_, ok := f.Get("nobody")
	if ok {
		t.Errorf("Get(nobody) 返回 true, 期望 false")
	}
}

// TestChannelFilter_Isolate 验证 Isolate 注入 context 且 FilterKeyFromContext 可提取。
func TestChannelFilter_Isolate(t *testing.T) {
	f := NewChannelFilter()
	ctx := f.Isolate(context.Background(), "u1", "ch1")

	k, ok := FilterKeyFromContext(ctx)
	if !ok {
		t.Fatalf("FilterKeyFromContext 返回 false, 期望 true")
	}
	if k.UserID != "u1" || k.Channel != "ch1" {
		t.Errorf("filterKey = {%s:%s}, 期望 {u1:ch1}", k.UserID, k.Channel)
	}
}

// TestChannelFilter_Isolate_ChannelSwitch 验证频道切换后 filterKey 更新，旧频道不污染。
func TestChannelFilter_Isolate_ChannelSwitch(t *testing.T) {
	f := NewChannelFilter()

	// 切到 ch1
	ctx1 := f.Isolate(context.Background(), "u1", "ch1")
	k1, _ := FilterKeyFromContext(ctx1)
	if k1.Channel != "ch1" {
		t.Errorf("ctx1 filterKey.Channel = %q, 期望 ch1", k1.Channel)
	}

	// 切到 ch2（同一用户切换频道）
	ctx2 := f.Isolate(context.Background(), "u1", "ch2")
	k2, _ := FilterKeyFromContext(ctx2)
	if k2.Channel != "ch2" {
		t.Errorf("ctx2 filterKey.Channel = %q, 期望 ch2", k2.Channel)
	}

	// ctx1 仍保持 ch1（不跨频道污染）
	if k1.Channel != "ch1" {
		t.Errorf("切换频道后 ctx1 filterKey.Channel 变为 %q, 期望仍为 ch1（隔离）", k1.Channel)
	}

	// Get 反映最新频道
	kLatest, _ := f.Get("u1")
	if kLatest.Channel != "ch2" {
		t.Errorf("Get(u1).Channel = %q, 期望 ch2（最新频道）", kLatest.Channel)
	}
}

// TestChannelFilter_Isolate_DifferentUsers 验证不同用户 filterKey 互不干扰。
func TestChannelFilter_Isolate_DifferentUsers(t *testing.T) {
	f := NewChannelFilter()
	ctxA := f.Isolate(context.Background(), "uA", "chA")
	ctxB := f.Isolate(context.Background(), "uB", "chB")

	kA, _ := FilterKeyFromContext(ctxA)
	kB, _ := FilterKeyFromContext(ctxB)

	if kA.String() != "uA:chA" {
		t.Errorf("uA filterKey = %q, 期望 uA:chA", kA.String())
	}
	if kB.String() != "uB:chB" {
		t.Errorf("uB filterKey = %q, 期望 uB:chB", kB.String())
	}
}

// TestFilterKeyFromContext_Empty 验证无 filterKey 的 context 返回 false。
func TestFilterKeyFromContext_Empty(t *testing.T) {
	_, ok := FilterKeyFromContext(context.Background())
	if ok {
		t.Errorf("空 context FilterKeyFromContext 返回 true, 期望 false")
	}
}
