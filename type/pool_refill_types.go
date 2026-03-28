package types

type PoolRefillJob struct {
	UserID       string   `json:"user_id"`
	PoolType     string   `json:"pool_type"`
	PeriodBucket string   `json:"period_bucket"`
	QueryTexts   []string `json:"query_texts"`
}

type PoolRefillEnqueueResult struct {
	Key         string `json:"key"`
	PoolType    string `json:"pool_type"`
	QueueResult string `json:"queue_result"`
	Enqueued    bool   `json:"enqueued"`
	Deduped     bool   `json:"deduped"`
	Dropped     bool   `json:"dropped"`
}

type PoolRefillRunResult struct {
	PoolType          string  `json:"pool_type"`
	PeriodBucket      string  `json:"period_bucket"`
	Inserted          int     `json:"inserted"`
	Considered        int     `json:"considered"`
	PoolSizeAfter     int     `json:"pool_size_after"`
	ReturnedDocCount  int     `json:"returned_doc_count"`
	CoverageScore     float32 `json:"coverage_score"`
	Empty             bool    `json:"empty"`
	FailedQueries     int     `json:"failed_queries"`
	SuccessfulQueries int     `json:"successful_queries"`
}
