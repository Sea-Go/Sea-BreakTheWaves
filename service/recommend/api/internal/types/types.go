package types

import (
	recommendclient "sea/service/recommend/rpc/recommendclient"
)

type (
	RecommendRequest   = recommendclient.RecommendRequest
	RecommendResponse  = recommendclient.RecommendResponse
	EventBatchRequest  = recommendclient.EventBatchRequest
	EventBatchResponse = recommendclient.EventBatchResponse
	TraceQueryRequest  = recommendclient.TraceQueryRequest
	TraceQueryResponse = any
	ObservationSummary = any
	EvaluationSummary  = any
)
