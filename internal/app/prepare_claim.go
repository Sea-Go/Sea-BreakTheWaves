// Package app assembles technical worker requests into framework runs.
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
)

var ErrInvalidPrepareJob = errors.New("invalid fixed content prepare job")
var ErrExpiredPrepareLease = errors.New("content prepare lease expired")

// DecodePrepareClaim maps a granted DC technical lease to the fixed RTW build
// input. The DC InputHash covers the complete Submit envelope; it is not the
// release manifest hash stored in BuildInput.InputHash.
func DecodePrepareClaim(job jobs.Job, workerID, jobType, resourceProfile string, now time.Time) (content.BuildInput, content.Fence, error) {
	var input content.BuildInput
	if workerID == "" || jobType == "" || resourceProfile == "" || job.ID == "" || !artifacts.ValidHash(job.InputHash) ||
		job.State != "running" || job.WorkerID != workerID ||
		job.Request.JobType != jobType || job.Request.ResourceProfile != resourceProfile ||
		job.AttemptID == "" || job.LeaseEpoch <= 0 || job.CancelVersion < 0 || job.Request.Producer == "" || job.Request.OperationID == "" {
		return input, content.Fence{}, ErrInvalidPrepareJob
	}
	decoder := json.NewDecoder(bytes.NewReader(job.Request.Input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return content.BuildInput{}, content.Fence{}, fmt.Errorf("%w: %v", ErrInvalidPrepareJob, err)
	}
	if decoder.Decode(new(any)) != io.EOF || input.BuildID == "" || input.ModuleID == "" || input.ReleaseID == "" ||
		input.Generation <= 0 || !artifacts.ValidHash(input.InputHash) || input.OperationID != job.Request.OperationID || len(input.Revisions) == 0 {
		return content.BuildInput{}, content.Fence{}, ErrInvalidPrepareJob
	}
	expires, err := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if err != nil {
		return content.BuildInput{}, content.Fence{}, fmt.Errorf("%w: invalid lease expiry", ErrInvalidPrepareJob)
	}
	if !expires.After(now) {
		return content.BuildInput{}, content.Fence{}, ErrExpiredPrepareLease
	}
	fence := content.Fence{BuildID: input.BuildID, AttemptID: job.AttemptID, LeaseEpoch: job.LeaseEpoch,
		CancelVersion: job.CancelVersion, ExpiresAt: expires}
	input.Revisions = append([]string(nil), input.Revisions...)
	return input, fence, nil
}
