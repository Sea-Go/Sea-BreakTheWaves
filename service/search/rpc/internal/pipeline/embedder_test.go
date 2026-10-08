package pipeline

import (
	"context"
	"testing"

	frameworkembedder "trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
)

// 编译期断言：经框架接口消费（测试走框架真实类型，而非仅本包方法）。
var _ frameworkembedder.Embedder = (*EncoderEmbedder)(nil)

// TestEncoderEmbedderFrameworkInterface 经框架接口编码文本（C17：编码
// 接缝以框架公开 API 为基础）。
func TestEncoderEmbedderFrameworkInterface(t *testing.T) {
	e, err := NewEncoderEmbedder(FakeEncoder{})
	if err != nil {
		t.Fatalf("NewEncoderEmbedder: %v", err)
	}
	vec, err := e.GetEmbedding(context.Background(), "海洋观测的核心要点")
	if err != nil {
		t.Fatalf("GetEmbedding: %v", err)
	}
	if len(vec) == 0 {
		t.Fatal("embedding is empty")
	}
	if dims := e.GetDimensions(); dims != len(vec) {
		t.Fatalf("GetDimensions=%d != len(embedding)=%d", dims, len(vec))
	}
}

// TestEncoderEmbedderDeterministic 同文本同向量。
func TestEncoderEmbedderDeterministic(t *testing.T) {
	e, _ := NewEncoderEmbedder(FakeEncoder{})
	first, err := e.GetEmbedding(context.Background(), "确定性检查")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := e.GetEmbedding(context.Background(), "确定性检查")
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != len(first) {
			t.Fatalf("run %d length drift", i)
		}
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("run %d value drift at %d", i, j)
			}
		}
	}
}

// TestEncoderEmbedderUsage usage 为 nil（dev 无用量信息）。
func TestEncoderEmbedderUsage(t *testing.T) {
	e, _ := NewEncoderEmbedder(FakeEncoder{})
	vec, usage, err := e.GetEmbeddingWithUsage(context.Background(), "x")
	if err != nil || len(vec) == 0 {
		t.Fatalf("GetEmbeddingWithUsage: vec=%v err=%v", vec, err)
	}
	if usage != nil {
		t.Fatalf("usage should be nil in dev form, got %v", usage)
	}
}

// TestEncoderEmbedderRejectsBadInput 空文本/取消的 ctx 明确报错。
func TestEncoderEmbedderRejectsBadInput(t *testing.T) {
	e, _ := NewEncoderEmbedder(FakeEncoder{})
	if _, err := e.GetEmbedding(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty text")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.GetEmbedding(ctx, "x"); err == nil {
		t.Fatal("expected error for cancelled ctx")
	}
}

// TestEncoderEmbedderRequiresEncoder nil 编码器即拒。
func TestEncoderEmbedderRequiresEncoder(t *testing.T) {
	if _, err := NewEncoderEmbedder(nil); err == nil {
		t.Fatal("expected error for nil encoder")
	}
}
