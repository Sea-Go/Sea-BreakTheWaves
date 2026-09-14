// Code generated from the DataCenter public wire contract. DO NOT EDIT.
package jobs

import "encoding/json"

type Submit struct {
	Producer        string          `json:"producer"`
	OperationID     string          `json:"operation_id"`
	RunRef          string          `json:"run_ref"`
	JobType         string          `json:"job_type"`
	ResourceProfile string          `json:"resource_profile"`
	Input           json.RawMessage `json:"input"`
	Deadline        string          `json:"deadline"`
	MaxAttempts     int             `json:"max_attempts"`
}
type Job struct {
	ID             string  `json:"job_id"`
	InputHash      string  `json:"input_hash"`
	Request        Submit  `json:"request"`
	State          string  `json:"technical_state"`
	Attempt        int     `json:"attempt"`
	LeaseEpoch     int64   `json:"lease_epoch"`
	CancelVersion  int64   `json:"cancel_version"`
	AttemptID      string  `json:"attempt_id,omitempty"`
	WorkerID       string  `json:"worker_id,omitempty"`
	LeaseExpiresAt string  `json:"lease_expires_at,omitempty"`
	Result         *Result `json:"result,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}
type Claim struct {
	WorkerID        string `json:"worker_id"`
	JobType         string `json:"job_type"`
	ResourceProfile string `json:"resource_profile"`
	LeaseSeconds    int    `json:"lease_seconds"`
}
type Lease struct {
	WorkerID      string `json:"worker_id"`
	AttemptID     string `json:"attempt_id"`
	LeaseEpoch    int64  `json:"lease_epoch"`
	CancelVersion int64  `json:"cancel_version"`
}
type Renew struct {
	Lease
	LeaseSeconds int `json:"lease_seconds"`
}
type ResultRef struct {
	URI       string `json:"uri"`
	Hash      string `json:"sha256"`
	MediaType string `json:"media_type"`
}
type Result struct {
	State     string     `json:"technical_state"`
	Ref       *ResultRef `json:"result_ref,omitempty"`
	ErrorCode string     `json:"error_code,omitempty"`
	Retryable bool       `json:"retryable"`
}
type Complete struct {
	Lease
	Result Result `json:"result"`
}
type CompletionReceipt struct {
	JobID          string `json:"job_id"`
	AttemptID      string `json:"attempt_id"`
	LeaseEpoch     int64  `json:"lease_epoch"`
	CancelVersion  int64  `json:"cancel_version"`
	ResultHash     string `json:"result_hash"`
	TechnicalState string `json:"technical_state"`
}
type Cancel struct {
	OperationID           string `json:"operation_id"`
	ExpectedCancelVersion int64  `json:"expected_cancel_version"`
	Reason                string `json:"reason"`
}
type CancelReceipt struct {
	JobID          string `json:"job_id"`
	OperationID    string `json:"operation_id"`
	CancelVersion  int64  `json:"cancel_version"`
	TechnicalState string `json:"technical_state"`
}

type SubmissionReceipt struct {
	ID              string `json:"job_id"`
	Producer        string `json:"producer"`
	OperationID     string `json:"operation_id"`
	InputHash       string `json:"input_hash"`
	TechnicalStatus string `json:"technical_status"`
	ReceivedAt      string `json:"received_at"`
}
