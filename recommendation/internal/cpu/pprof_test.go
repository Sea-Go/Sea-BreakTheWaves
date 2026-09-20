// Package cpu pprof_test.go — PProfServer 单元测试（Task 13.2）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - Start/Stop 生命周期
//   - pprof 端点可访问（/debug/pprof/ 与 /debug/pprof/heap）
//   - 未启动时 Stop 幂等返回 nil
//   - ctx 取消自动触发 Shutdown
//   - 重复 Start 返回错误
package cpu

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestPProfServer_StartStop 验证 Start/Stop 生命周期与端点可访问。
func TestPProfServer_StartStop(t *testing.T) {
	srv := NewPProfServer("127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start 错误: %v", err)
	}
	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr 为空")
	}

	// 验证 pprof index 端点可访问。
	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/ 错误: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("index status = %d, 期望 200", resp.StatusCode)
	}

	// 验证 heap 端点可访问。
	resp2, err := http.Get("http://" + addr + "/debug/pprof/heap")
	if err != nil {
		t.Fatalf("GET /debug/pprof/heap 错误: %v", err)
	}
	defer resp2.Body.Close()
	_, _ = io.Copy(io.Discard, resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("heap status = %d, 期望 200", resp2.StatusCode)
	}

	// 验证 goroutine 端点可访问。
	resp3, err := http.Get("http://" + addr + "/debug/pprof/goroutine")
	if err != nil {
		t.Fatalf("GET /debug/pprof/goroutine 错误: %v", err)
	}
	defer resp3.Body.Close()
	_, _ = io.Copy(io.Discard, resp3.Body)
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("goroutine status = %d, 期望 200", resp3.StatusCode)
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop 错误: %v", err)
	}
}

// TestPProfServer_StopBeforeStart 验证未启动时 Stop 幂等返回 nil。
func TestPProfServer_StopBeforeStart(t *testing.T) {
	srv := NewPProfServer(":0")
	if err := srv.Stop(); err != nil {
		t.Errorf("未启动时 Stop 应返回 nil, 实际: %v", err)
	}
}

// TestPProfServer_DoubleStop 验证重复 Stop 幂等返回 nil。
func TestPProfServer_DoubleStop(t *testing.T) {
	srv := NewPProfServer("127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start 错误: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("第一次 Stop 错误: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Errorf("第二次 Stop 应返回 nil, 实际: %v", err)
	}
}

// TestPProfServer_DoubleStart 验证重复 Start 返回错误。
func TestPProfServer_DoubleStart(t *testing.T) {
	srv := NewPProfServer("127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("第一次 Start 错误: %v", err)
	}
	if err := srv.Start(ctx); err == nil {
		t.Error("重复 Start 应返回错误")
	}
	_ = srv.Stop()
}

// TestPProfServer_CtxCancel 验证 ctx 取消自动触发 Shutdown。
func TestPProfServer_CtxCancel(t *testing.T) {
	srv := NewPProfServer("127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start 错误: %v", err)
	}
	addr := srv.Addr()

	// 取消 ctx 应触发 Shutdown。
	cancel()

	// 轮询等待 server 关闭（最多 2s）。
	deadline := time.Now().Add(2 * time.Second)
	closed := false
	for time.Now().Before(deadline) {
		client := http.Client{Timeout: 100 * time.Millisecond}
		_, err := client.Get("http://" + addr + "/debug/pprof/")
		if err != nil {
			closed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !closed {
		t.Error("ctx 取消后 server 应关闭（请求应失败）")
	}

	// 再次 Stop 应返回 nil（幂等）。
	if err := srv.Stop(); err != nil {
		t.Errorf("ctx 取消后再 Stop 应返回 nil, 实际: %v", err)
	}
}

// TestPProfServer_DefaultAddr 验证空串地址默认为 ":6060"。
func TestPProfServer_DefaultAddr(t *testing.T) {
	srv := NewPProfServer("")
	// 未启动时 Addr() 返回配置的 addr。
	if got := srv.Addr(); got != ":6060" {
		t.Errorf("默认 addr = %q, 期望 :6060", got)
	}
}

// TestPProfServer_NilReceiver 验证 nil receiver 安全处理。
func TestPProfServer_NilReceiver(t *testing.T) {
	var srv *PProfServer
	if err := srv.Start(context.Background()); err == nil {
		t.Error("nil receiver Start 应返回错误")
	}
	if err := srv.Stop(); err == nil {
		t.Error("nil receiver Stop 应返回错误")
	}
	if got := srv.Addr(); got != "" {
		t.Errorf("nil receiver Addr 应返回空串, 实际 %q", got)
	}
}
