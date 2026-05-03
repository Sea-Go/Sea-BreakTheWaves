package types

type dashscopeRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
	Instruct  string   `json:"instruct,omitempty"`
}

type dashscopeRerankResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
			Document       struct {
				Text string `json:"text"`
			} `json:"document"`
		} `json:"results"`
	} `json:"output"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
