package logic

import (
	"context"
	"encoding/json"
)

type EventLogic struct{}

func NewEventLogic() *EventLogic { return &EventLogic{} }

func (l *EventLogic) Submit(ctx context.Context, contentType string, body []byte) (map[string]any, error) {
	var batch struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, err
	}
	return map[string]any{"accepted": len(batch.Events)}, nil
}
