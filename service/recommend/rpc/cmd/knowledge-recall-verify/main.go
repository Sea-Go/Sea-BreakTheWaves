// knowledge-recall-verify:一次性验证「知识模块内容进入推荐召回链」。
// 在家里云 arch-server 上执行(环境变量):
//   KNOWLEDGE_SERVING_URL/TOKEN/GRAPH_ID(必填)、VERIFY_QUERY(默认 TCP)
// 经 HybridRecaller(生产召回编排器)执行真实召回——source=knowledge 候选
// 出现即验证通过;生产装配代码(recommendclient.NewRuntime)已同时接线。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/model/recall"
)

func main() {
	url := os.Getenv("KNOWLEDGE_SERVING_URL")
	token := os.Getenv("KNOWLEDGE_SERVING_TOKEN")
	graphID := os.Getenv("KNOWLEDGE_GRAPH_ID")
	query := os.Getenv("VERIFY_QUERY")
	if query == "" {
		query = "TCP"
	}
	if url == "" || token == "" || graphID == "" {
		fmt.Fprintln(os.Stderr, "缺 KNOWLEDGE_SERVING_URL/TOKEN/GRAPH_ID")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 与生产编排一致:知识源作为 Hybrid 的一个真实召回源。
	hybrid, err := recall.NewHybridRecaller([]domain.Recaller{
		recall.NewKnowledgeRecaller(url, token, graphID, 10),
	}, 4, 20)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Hybrid 组装失败:", err)
		os.Exit(1)
	}
	result, err := hybrid.Recall(ctx, domain.RecallRequest{
		TopK:   20,
		Intent: &domain.Intent{Signals: map[string]string{"query": query}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "召回失败:", err)
		os.Exit(1)
	}
	fmt.Printf("召回源 %s:共 %d 个候选(检索词 %q)\n", result.Source, len(result.Candidates), query)
	knowledgeSeen := 0
	for _, c := range result.Candidates {
		if c.Source != "knowledge" {
			continue
		}
		knowledgeSeen++
		extra, _ := json.Marshal(c.Extra)
		fmt.Printf("  [knowledge] %s score=%.2f %s\n", c.ArticleID, c.Score, extra)
	}
	if knowledgeSeen == 0 {
		fmt.Println("VERIFY_FAIL: 知识源未产出候选")
		os.Exit(1)
	}
	fmt.Println("VERIFY_OK: 模块内容已进入推荐召回链(source=knowledge)")
}
