package function

import (
	"context"
	"fmt"

	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

type EvidenceRequest struct {
	Query string   `json:"query"`
	Limit int      `json:"limit"`
	IDs   []string `json:"ids"`
}

type EvidenceResponse struct {
	Items []EvidenceItem `json:"items"`
}

type EvidenceItem struct {
	ArticleID string  `json:"article_id"`
	Title     string  `json:"title"`
	Score     float64 `json:"score"`
}

type EvidenceReader interface {
	ReadEvidence(ctx context.Context, req EvidenceRequest) (EvidenceResponse, error)
}

// NewEvidenceSearch adapts a Search-owned reader into a typed framework Tool.
func NewEvidenceSearch(reader EvidenceReader) (trpctool.Tool, error) {
	if reader == nil {
		return nil, fmt.Errorf("evidence reader is required")
	}
	return function.NewFunctionTool(
		func(ctx context.Context, in EvidenceRequest) (EvidenceResponse, error) {
			if in.Query == "" && len(in.IDs) == 0 {
				return EvidenceResponse{}, fmt.Errorf("query or ids required")
			}
			if in.Limit <= 0 || in.Limit > 100 {
				in.Limit = 10
			}
			return reader.ReadEvidence(ctx, in)
		},
		function.WithName("search_evidence"),
		function.WithDescription("Read evidence candidates owned by the Search service."),
	), nil
}
