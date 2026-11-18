package recall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// KnowledgeRecaller 把正式知识模块(课程内容)的当前发布图接入推荐召回链:
// 按意图信号(query/keyword)检索知识图单元,产出真实候选(ArticleID=单元
// node_id,Source=knowledge),Extra 携带模块/定位/版本可观测元数据。
// 端点缺配置时不应被构造(装配侧守卫),构造后失败按召回源独立降级返回空。
type KnowledgeRecaller struct {
	endpoint   string // knowledge-serving 基址,如 http://127.0.0.1:19311
	token      string
	graphID    string
	topK       int
	httpClient *http.Client
}

// knowledge-serving /graph/query 的节点是裸属性 map(直接含 node_id)。
type knowledgeQueryResponse struct {
	Nodes []map[string]any `json:"nodes"`
}

func NewKnowledgeRecaller(endpoint, token, graphID string, topK int) *KnowledgeRecaller {
	if topK <= 0 || topK > 50 {
		topK = 10
	}
	return &KnowledgeRecaller{
		endpoint:   endpoint,
		token:      token,
		graphID:    graphID,
		topK:       topK,
		httpClient: &http.Client{Timeout: 8 * time.Second},
	}
}

func (k *KnowledgeRecaller) Name() string { return "knowledge" }

// Recall 实现知识召回:意图信号里的 query/keyword/entity 作为检索词;
// 无检索词时按图谱顺序返回课程单元(冷启动仍可发现课程内容)。
func (k *KnowledgeRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	result := domain.RecallResult{Source: k.Name(), Candidates: []domain.Candidate{}}
	if k == nil || k.endpoint == "" {
		return result, nil
	}
	keyword := ""
	if req.Intent != nil {
		for _, key := range []string{"query", "keyword", "entity"} {
			if v, ok := req.Intent.Signals[key]; ok && v != "" {
				keyword = v
				break
			}
		}
	}
	body := map[string]any{
		"graph_id":  k.graphID,
		"node_type": "unit",
		"keyword":   keyword,
		"hops":      0,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, k.endpoint+"/graph/query", bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+k.token)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := k.httpClient.Do(httpReq)
	if err != nil {
		// 召回源独立降级:知识服务不可用不阻断其它召回源。
		return result, fmt.Errorf("knowledge recall endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("knowledge recall status %d", resp.StatusCode)
	}
	var parsed knowledgeQueryResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return result, err
	}
	for i, props := range parsed.Nodes {
		id, _ := props["node_id"].(string)
		if id == "" {
			continue
		}
		candidate := domain.Candidate{
			ArticleID: id,
			// 召回分:按返回序递减(图谱顺序≈课程结构顺序),量级与其它源兼容。
			Score:  0.9 - float64(i)*0.02,
			Source: k.Name(),
			Scores: map[string]float64{"knowledge_score": 0.9 - float64(i)*0.02},
			Extra: map[string]any{
				"knowledge_module": true,
				"graph_id":         k.graphID,
				"revision_id":      props["revision_id"],
				"node_type":        "unit",
				"title":            props["title"],
			},
		}
		result.Candidates = append(result.Candidates, candidate)
		if len(result.Candidates) >= k.topK {
			break
		}
	}
	return result, nil
}

