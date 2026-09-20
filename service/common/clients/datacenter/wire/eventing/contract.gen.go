// Code generated from the DataCenter public wire contract. DO NOT EDIT.
package eventing

import "encoding/json"

type Event struct {
	EventID          string          `json:"event_id"`
	EventType        string          `json:"event_type"`
	SchemaVersion    int             `json:"schema_version"`
	Producer         string          `json:"producer"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateVersion int64           `json:"aggregate_version"`
	OperationID      string          `json:"operation_id"`
	OccurredAt       string          `json:"occurred_at"`
	Payload          json.RawMessage `json:"payload"`
}
type Receipt struct {
	EventID         string `json:"event_id"`
	Producer        string `json:"producer"`
	TechnicalStatus string `json:"technical_status"`
	ReceiptID       string `json:"receipt_id"`
	InputHash       string `json:"input_hash"`
	Offset          int64  `json:"offset"`
	ReceivedAt      string `json:"received_at"`
}
type Item struct {
	Offset    int64  `json:"offset"`
	InputHash string `json:"input_hash"`
	Event     Event  `json:"event"`
}
type Batch struct {
	Consumer   string `json:"consumer"`
	Producer   string `json:"producer"`
	FromOffset int64  `json:"from_offset"`
	ToOffset   int64  `json:"to_offset"`
	BatchHash  string `json:"batch_hash"`
	Events     []Item `json:"events"`
}
type Acknowledge struct {
	Producer   string `json:"producer"`
	FromOffset int64  `json:"from_offset"`
	ToOffset   int64  `json:"to_offset"`
	BatchHash  string `json:"batch_hash"`
}
type DeliveryReceipt struct {
	Consumer           string `json:"consumer"`
	Producer           string `json:"producer"`
	AcknowledgedOffset int64  `json:"acknowledged_offset"`
	TechnicalStatus    string `json:"technical_status"`
}

type SourceWatermark struct {
	Producer          string `json:"producer"`
	AggregateID       string `json:"aggregate_id"`
	ContiguousVersion int64  `json:"contiguous_version"`
	MaxSeenVersion    int64  `json:"max_seen_version"`
	HasGap            bool   `json:"has_gap"`
}
