// Command indexer 是 M2 indexer 三段 worker 的 dev 形态可运行入口（对应
// 部署形态 dev 的 btw-worker-all）：从 JSONL 事件文件读 C-1 镜像事件，用
// 确定性假编码器（fake.go）跑完整管线——工件在内存 Sink 落位并打印轨迹，
// C-2 READY 回执打印到 stdout。传输接缝（真实 DC 事件、对象存储、编码
// 服务）后续替换，管线本体见 internal/indexer。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/indexer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "indexer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	events := flag.String("events", "", "JSONL 事件文件路径（每行一个 C-1 release 事件，必填）")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runAll(ctx, *events, os.Stdout)
}

// runAll 装配各接缝的内存实现并跑完整管线；w 接收工件轨迹与 READY 回执
// （测试注入 buffer，dev 形态为 stdout）。
func runAll(ctx context.Context, eventsPath string, w io.Writer) error {
	if eventsPath == "" {
		return errors.New("--events is required（JSONL 事件文件路径，见 --help）")
	}
	events, err := readEvents(eventsPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "indexer dev: events=%s count=%d encoder=%s\n", eventsPath, len(events), fakeEncoderID)
	sink := newMemSink(w)
	p := &indexer.Pipeline{
		Source:   &sliceSource{events: events},
		Encoder:  fakeEncoder{},
		Sink:     sink,
		Receipts: printReceipts{w: w},
		Seen:     indexer.NewMemSeen(),
	}
	if err := p.Run(ctx); err != nil {
		return err
	}
	fmt.Fprintf(w, "indexer dev: done lines=%d artifacts=%d\n", len(events), sink.len())
	return nil
}

// readEvents 逐行解析 JSONL 事件文件，空行跳过，坏行带行号报错。
func readEvents(path string) ([]indexer.ReleaseEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open events: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	var events []indexer.ReleaseEvent
	line := 0
	for sc.Scan() {
		line++
		s := bytes.TrimSpace(sc.Bytes())
		if len(s) == 0 {
			continue
		}
		var ev indexer.ReleaseEvent
		if err := json.Unmarshal(s, &ev); err != nil {
			return nil, fmt.Errorf("events line %d: %w", line, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return events, nil
}

// sliceSource 是 EventSource 的内存实现：顺序弹出，空了返回 io.EOF。
type sliceSource struct {
	events []indexer.ReleaseEvent
}

func (s *sliceSource) Next(ctx context.Context) (indexer.ReleaseEvent, error) {
	if err := ctx.Err(); err != nil {
		return indexer.ReleaseEvent{}, err
	}
	if len(s.events) == 0 {
		return indexer.ReleaseEvent{}, io.EOF
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}

// memSink 是 Sink 的内存实现，落位同时打印工件轨迹。
type memSink struct {
	mu      sync.Mutex
	objects map[string][]byte
	w       io.Writer
}

func newMemSink(w io.Writer) *memSink {
	return &memSink{objects: make(map[string][]byte), w: w}
}

func (s *memSink) Put(_ context.Context, key string, b []byte) error {
	s.mu.Lock()
	s.objects[key] = append([]byte(nil), b...)
	s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "artifact %s %dB\n", key, len(b)); err != nil {
		return err
	}
	return nil
}

func (s *memSink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// printReceipts 是 ReceiptSink 的 dev 实现：打印 C-2 READY 回执。
type printReceipts struct {
	w io.Writer
}

func (r printReceipts) Ready(_ context.Context, manifestID string) error {
	_, err := fmt.Fprintf(r.w, "READY manifest_id=%s\n", manifestID)
	return err
}
