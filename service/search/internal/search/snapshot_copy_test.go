package search

import "testing"

func TestCloneSnapshotPreservesEmptyRevisionArray(t *testing.T) {
	original := snapshot()
	original.ValidRevisionIDs = []string{}
	copied := cloneSnapshot(original)
	if copied.ValidRevisionIDs == nil || len(copied.ValidRevisionIDs) != 0 {
		t.Fatal("RTW empty valid_revision_ids array changed to null")
	}
}
