package usermodel

import (
	"context"
	"errors"
	"testing"
)

func TestCurrentReceiptPromotedPendingRequiresCommittedOutbox(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	later := fixture("later-current-receipt", 2)
	later.Action = Correct
	later.Supersedes = &EventKey{Producer: later.Producer, EventID: "first-current-receipt"}
	initial := requireAppend(t, s, later)
	if initial.Status != "pending_dependency" || initial.StateVersion != 0 {
		t.Fatalf("first pending receipt: %+v", initial)
	}
	before, err := s.CurrentReceipt(ctx, later.Subject, later.EventKey)
	if err != nil || before.Status != "pending_dependency" || before.StateVersion != 0 || before.NormalizedHash != initial.NormalizedHash {
		t.Fatalf("pending current receipt: %+v %v", before, err)
	}
	first := fixture("first-current-receipt", 1)
	requireAppend(t, s, first)
	replayed, err := s.Append(ctx, later)
	if err != nil || !replayed.Replay || replayed.Status != "pending_dependency" || replayed.StateVersion != 0 {
		t.Fatalf("original replay receipt changed: %+v %v", replayed, err)
	}
	current, err := s.CurrentReceipt(ctx, later.Subject, later.EventKey)
	if err != nil || current.Status != "accepted" || current.StateVersion != 2 || current.NormalizedHash != initial.NormalizedHash || current.Replay {
		t.Fatalf("promoted current receipt: %+v %v", current, err)
	}
	other := later.Subject
	other.TenantID = "another-tenant"
	if _, err := s.CurrentReceipt(ctx, other, later.EventKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant receipt leaked: %v", err)
	}
	missing := EventKey{Producer: later.Producer, EventID: "missing"}
	if _, err := s.CurrentReceipt(ctx, later.Subject, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing receipt accepted: %v", err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM usermodel_outbox WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3
		AND producer=$4 AND event_id=$5`, later.Subject.AuthorityID, later.Subject.TenantID,
		later.Subject.SubjectID, later.Producer, later.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CurrentReceipt(ctx, later.Subject, later.EventKey); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted fact without Outbox falsely acknowledged: %v", err)
	}
}
