package graph

// State keys are deliberately typed and documented so nodes cannot overload a
// generic map value. Search-specific keys are owned by this package only.
const (
	StateKeyQuery        = "search.query"
	StateKeyIntent       = "search.intent"
	StateKeyCandidates   = "search.candidates"
	StateKeyEvidence     = "search.evidence"
	StateKeyResponseText = "search.response_text"
	StateKeyBudget       = "search.budget"
)
