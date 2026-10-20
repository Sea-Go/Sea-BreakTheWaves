// embedder.go —— 框架 knowledge/embedder.Embedder 的装配层（C17）。
//
// 框架的公开编码接缝是 GetEmbedding(ctx, text) ([]float64, error)（另含
// usage 与维度两个可选面）。此前管线的 QueryEncoder 是自定义接缝，未实现
// 框架接口（2026-10-08 复用审计确认的空白）。本文件把 QueryEncoder 适配
// 到框架接口：dense 路向量即"该文本的嵌入"，sparse/multi 两路仍属管线
// 内部表示（框架接口只关心单一向量）。
//
// 真实化路径：QueryEncoder 换 DC 网关的真实编码器后，本适配无需改动——
// 框架侧看到的仍是"文本 → 向量"。
package pipeline

import (
	"context"
	"fmt"
	"sync"

	frameworkembedder "trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
)

// 编译期断言：本适配器完整实现框架 Embedder 接口。
var _ frameworkembedder.Embedder = (*EncoderEmbedder)(nil)

// ErrEmbedderInput 标记编码输入非法。
var ErrEmbedderInput = fmt.Errorf("pipeline embedder: invalid input")

// EncoderEmbedder 把管线查询编码器适配为框架 Embedder。零值不可用。
type EncoderEmbedder struct {
	encoder QueryEncoder

	mu       sync.Mutex
	dims     int
	dimsOnce bool
}

// NewEncoderEmbedder 构造框架编码器。encoder 为 nil 即拒。
func NewEncoderEmbedder(encoder QueryEncoder) (*EncoderEmbedder, error) {
	if encoder == nil {
		return nil, fmt.Errorf("%w: encoder is required", ErrEmbedderInput)
	}
	return &EncoderEmbedder{encoder: encoder}, nil
}

// GetEmbedding 编码文本为向量（dense 路，float64 口径）。
func (e *EncoderEmbedder) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if text == "" {
		return nil, fmt.Errorf("%w: text is required", ErrEmbedderInput)
	}
	dense := e.encoder.EncodeQuery(text).Dense
	out := make([]float64, len(dense))
	for i, v := range dense {
		out[i] = float64(v)
	}
	return out, nil
}

// GetEmbeddingWithUsage 编码并返回使用信息（dev 形态无 usage，返回 nil）。
func (e *EncoderEmbedder) GetEmbeddingWithUsage(ctx context.Context, text string) ([]float64, map[string]any, error) {
	vec, err := e.GetEmbedding(ctx, text)
	if err != nil {
		return nil, nil, err
	}
	return vec, nil, nil
}

// GetDimensions 返回编码维度（首次调用以探针文本实测并缓存；探测失败
// 返回 0，符合框架"未知维度返回 0"的约定）。
func (e *EncoderEmbedder) GetDimensions() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dimsOnce {
		return e.dims
	}
	e.dimsOnce = true
	if e.encoder == nil {
		return 0
	}
	e.dims = len(e.encoder.EncodeQuery("\x00sea-dimension-probe").Dense)
	return e.dims
}
