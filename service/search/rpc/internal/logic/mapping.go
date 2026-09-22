package logic

import (
	searchpb "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/pb"
	searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"
)

func mapSearchRequest(in *searchpb.SearchRequest) searchclient.ContentSearchRequest {
	return searchclient.ContentSearchRequest{SearchRequestID: in.SearchRequestId, RequestID: in.SearchRequestId,
		Query: in.Query, TopK: int(in.TopK), Explain: in.Explain}
}

func mapSearchResponse(out searchclient.ContentSearchResponse) *searchpb.SearchResponse {
	resp := &searchpb.SearchResponse{TraceId: out.TraceID, SearchRequestId: out.SearchRequestID,
		Status: out.Status, Answer: "", ErrorMessage: ""}
	for _, item := range out.Items {
		resp.Hits = append(resp.Hits, &searchpb.SearchHit{ArticleId: item.ArticleID, Title: item.Title,
			Snippet: item.Snippet, Score: float64(item.MatchScore)})
	}
	return resp
}
