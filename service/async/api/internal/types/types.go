package types

type EventBatch struct {
	Events []jsonRawMessage `json:"events"`
}

type jsonRawMessage = []byte
