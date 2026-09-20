// Package cpu pprof.go — pprof HTTP 端点（Task 13.2）。
//
// 该文件实现 PProfServer：基于 net/http/pprof 标准库暴露 CPU/heap/goroutine
// profile 端点，供持续火焰图采样与线上诊断。
//
// 职责：
//   - 启动 HTTP server，注册 pprof handlers 到独立 mux（不污染 DefaultServeMux）
//   - 提供优雅关闭（server.Shutdown）
//   - 端点：/debug/pprof/（index）、/debug/pprof/profile（CPU profile）、
//     /debug/pprof/heap（heap）、/debug/pprof/goroutine
//
// 二开扩展点：
//   - 通过 NewPProfServer(addr) 注入自定义监听地址
//   - 通过 Start(ctx) 集成到主流程生命周期（ctx 取消自动触发 Shutdown）
//
// 不直接 import 第三方 pprof 库，net/http/pprof 为标准库。
package cpu

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"
)

// PProfServer pprof HTTP 端点服务。
//
// 职责：暴露 Go runtime profile 端点供火焰图采样与线上诊断。
// 集成 net/http/pprof 标准库，但用独立 mux 承载 handlers，避免污染全局
// DefaultServeMux。
//
// 字段语义：
//   - addr：配置的监听地址（如 ":6060"）
//   - mux：独立 ServeMux（注册 pprof handlers）
//   - server：底层 *http.Server（支持 Shutdown），nil 表示未启动
//   - listener：底层 net.Listener（用于 Addr() 返回实际端口）
//   - mu：保护 server/listener 并发读写
//
// 二开扩展点：通过 NewPProfServer(addr) 注入地址；通过 Start/Stop 管理生命周期。
type PProfServer struct {
	// addr 配置的监听地址（host:port）。
	addr string
	// mux 独立 ServeMux，承载 pprof handlers。
	mux *http.ServeMux
	// server 底层 HTTP server，nil 表示未启动。
	server *http.Server
	// listener 底层 net.Listener，用于获取实际监听地址（端口 0 场景）。
	listener net.Listener
	// mu 保护 server/listener 并发读写（ctx 取消 goroutine 与 Stop() 可能并发）。
	mu sync.Mutex
}

// NewPProfServer 构造 PProfServer。
// addr 监听地址（如 ":6060"），空串默认 ":6060"。
// 返回 *PProfServer（未启动，需调用 Start 启动）。
func NewPProfServer(addr string) *PProfServer {
	if addr == "" {
		addr = ":6060"
	}
	mux := http.NewServeMux()
	// 注册 pprof 端点（从 net/http/pprof 包复制 handler 到独立 mux，
	// 避免 import 副作用污染 DefaultServeMux 影响业务路由）。
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	// /debug/pprof/heap 与 /debug/pprof/goroutine 通过 pprof.Handler 复用。
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	return &PProfServer{
		addr: addr,
		mux:  mux,
	}
}

// Start 启动 pprof HTTP 端点（非阻塞）。
//
// 流程：
//  1. net.Listen 同步绑定端口（保证 Start 返回后端口已可接受连接）
//  2. 在 goroutine 中 server.Serve(listener)
//  3. 在 goroutine 中监听 ctx.Done()，触发时调用 Stop()
//
// 重复调用返回错误。ctx 取消后自动 Shutdown，调用方可不显式 Stop。
func (p *PProfServer) Start(ctx context.Context) error {
	if p == nil {
		return errors.New("pprof: nil receiver")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return errors.New("pprof: already started")
	}
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("pprof: listen %s: %w", p.addr, err)
	}
	p.listener = ln
	// 用局部变量 server 在 goroutine 中引用，避免 goroutine 读 p.server 与 Stop 写 p.server 竞争。
	server := &http.Server{
		Handler: p.mux,
	}
	p.server = server
	// goroutine: Serve 直到 Shutdown（通过局部变量引用 server，不读 p.server）。
	go func() {
		serveErr := server.Serve(ln)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			// 错误只能吞掉（无 log 依赖）；上层通过健康检查感知。
			_ = serveErr
		}
	}()
	// goroutine: 监听 ctx 取消，触发 Stop（幂等）。
	go func() {
		<-ctx.Done()
		_ = p.Stop()
	}()
	return nil
}

// Stop 优雅关闭 pprof HTTP 端点。
//
// 调用 server.Shutdown（5s 超时）；未启动时返回 nil；重复调用幂等返回 nil。
func (p *PProfServer) Stop() error {
	if p == nil {
		return errors.New("pprof: nil receiver")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.server.Shutdown(shutdownCtx)
	p.server = nil
	p.listener = nil
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("pprof: shutdown: %w", err)
	}
	return nil
}

// Addr 返回实际监听地址。
//
// 已启动时返回 listener 实际地址（端口 0 时为 OS 分配端口）；
// 未启动时返回配置的 addr。
func (p *PProfServer) Addr() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener != nil {
		return p.listener.Addr().String()
	}
	return p.addr
}
