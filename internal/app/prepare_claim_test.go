package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
)

func testPrepareJob(t *testing.T, now time.Time) jobs.Job {
	t.Helper()
	input := content.BuildInput{BuildID: "build-1", ModuleID: "module-1", ReleaseID: "release-1", Generation: 1,
		InputHash: strings.Repeat("a", 64), OperationID: "prepare-1", Revisions: []string{"revision-1"}}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return jobs.Job{ID: "job-1", InputHash: strings.Repeat("b", 64), State: "running", AttemptID: "attempt-1", WorkerID: "worker-1",
		LeaseEpoch: 2, LeaseExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		Request: jobs.Submit{OperationID: "prepare-1", JobType: "content.prepare.v1", ResourceProfile: "cpu", Input: raw}}
}

func TestDecodePrepareClaimBindsFixedAttempt(t *testing.T) {
	now := time.Now().UTC()
	job := testPrepareJob(t, now)
	input, fence, err := DecodePrepareClaim(job, "worker-1", "content.prepare.v1", "cpu", now)
	if err != nil || input.OperationID != "prepare-1" || fence.BuildID != input.BuildID ||
		fence.AttemptID != job.AttemptID || fence.LeaseEpoch != job.LeaseEpoch || !fence.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("input=%+v fence=%+v err=%v", input, fence, err)
	}
	input.Revisions[0] = "mutated"
	var original content.BuildInput
	if err := json.Unmarshal(job.Request.Input, &original); err != nil || original.Revisions[0] != "revision-1" {
		t.Fatal("decoded revision set aliases the job payload", err)
	}
}

func TestDecodePrepareClaimRejectsForeignOrExpiredLease(t *testing.T) {
	now := time.Now().UTC()
	for _, mutate := range []func(*jobs.Job){
		func(j *jobs.Job) { j.WorkerID = "other" },
		func(j *jobs.Job) { j.State = "cancel_requested" },
		func(j *jobs.Job) { j.Request.OperationID = "other" },
		func(j *jobs.Job) { j.Request.Input = []byte(`{"build_id":"b","unexpected":true}`) },
		func(j *jobs.Job) { j.Request.Input = append(j.Request.Input, []byte(` {}`)...) },
	} {
		job := testPrepareJob(t, now)
		mutate(&job)
		if _, _, err := DecodePrepareClaim(job, "worker-1", "content.prepare.v1", "cpu", now); !errors.Is(err, ErrInvalidPrepareJob) {
			t.Fatalf("foreign/malformed claim accepted: %v", err)
		}
	}
	job := testPrepareJob(t, now)
	job.LeaseExpiresAt = now.Format(time.RFC3339Nano)
	if _, _, err := DecodePrepareClaim(job, "worker-1", "content.prepare.v1", "cpu", now); !errors.Is(err, ErrExpiredPrepareLease) {
		t.Fatalf("expired lease accepted: %v", err)
	}
}
