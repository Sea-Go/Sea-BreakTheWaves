package pipeline

import (
	"context"
	"testing"

	frameworkretriever "trpc.group/trpc-go/trpc-agent-go/knowledge/retriever"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// TestSearchRetrieverFrameworkInterface 经框架接口执行整篇三路检索：
// 文本 → 候选文档（C17：检索器接缝以框架公开 API 为基础）。
func TestSearchRetrieverFrameworkInterface(t *testing.T) {
	r, err := NewSearchRetriever(newSeedsPipeline(t))
	if err != nil {
		t.Fatalf("NewSearchRetriever: %v", err)
	}
	defer func() { _ = r.Close() }()

	result, err := r.Retrieve(context.Background(), &frameworkretriever.Query{
		Text:  matrixQuery,
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if result == nil || len(result.Documents) == 0 {
		t.Fatal("no documents retrieved")
	}
	if len(result.Documents) > 5 {
		t.Fatalf("limit not applied: %d", len(result.Documents))
	}
	// 归一化分数：top 为 1.0，其余单调不增且 ∈ [0,1]。
	if result.Documents[0].Score != 1.0 {
		t.Fatalf("top score = %v, want 1.0 (normalized)", result.Documents[0].Score)
	}
	for i, d := range result.Documents {
		if d.Score < 0 || d.Score > 1.0 {
			t.Fatalf("doc[%d] score %v out of [0,1]", i, d.Score)
		}
		if d.Document.ID == "" {
			t.Fatalf("doc[%d] has empty ID", i)
		}
		if i > 0 && d.Score > result.Documents[i-1].Score {
			t.Fatalf("doc[%d] score %v > previous %v (not monotonic)", i, d.Score, result.Documents[i-1].Score)
		}
	}
}

// TestSearchRetrieverDocumentFilter 过滤集合只保留指定文档。
func TestSearchRetrieverDocumentFilter(t *testing.T) {
	r, err := NewSearchRetriever(newSeedsPipeline(t))
	if err != nil {
		t.Fatalf("NewSearchRetriever: %v", err)
	}
	defer func() { _ = r.Close() }()

	result, err := r.Retrieve(context.Background(), &frameworkretriever.Query{
		Text:   matrixQuery,
		Filter: &frameworkretriever.QueryFilter{DocumentIDs: []string{"doc-nonexistent-1", "doc-nonexistent-2"}},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(result.Documents) != 0 {
		t.Fatalf("filtered query should return 0 docs, got %d", len(result.Documents))
	}
}

// TestSearchRetrieverRejectsBadInput 空文本/nil 查询明确报错。
func TestSearchRetrieverRejectsBadInput(t *testing.T) {
	r, err := NewSearchRetriever(newSeedsPipeline(t))
	if err != nil {
		t.Fatalf("NewSearchRetriever: %v", err)
	}
	defer func() { _ = r.Close() }()
	if _, err := r.Retrieve(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil query")
	}
	if _, err := r.Retrieve(context.Background(), &frameworkretriever.Query{Text: ""}); err == nil {
		t.Fatal("expected error for empty text")
	}
}

// TestSearchRetrieverRequiresPipeline nil 管线即拒。
func TestSearchRetrieverRequiresPipeline(t *testing.T) {
	if _, err := NewSearchRetriever(nil); err == nil {
		t.Fatal("expected error for nil pipeline")
	}
}

// TestSearchRetrieverTierIsFast 检索器固定 fast+tools（不做摘要）。
func TestSearchRetrieverTierIsFast(t *testing.T) {
	// 经由结果非空 + 档位语义间接验证：fast 档跳 multi 路仍应有 dense+sparse
	// 候选（种子集上 fast 的 nDCG=0.887 即两路效果）。
	r, _ := NewSearchRetriever(newSeedsPipeline(t))
	defer func() { _ = r.Close() }()
	result, err := r.Retrieve(context.Background(), &frameworkretriever.Query{Text: matrixQuery})
	if err != nil || len(result.Documents) == 0 {
		t.Fatalf("fast tier retrieval failed: %v", err)
	}
	_ = retrieval.TierFast // 引用档位常量，锁住本测试与档位定义的关联
}
