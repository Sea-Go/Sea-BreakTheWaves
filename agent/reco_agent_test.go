package agent

import "testing"

func TestFilterExcludedArticleIDs(t *testing.T) {
	got := filterExcludedArticleIDs(
		[]string{"a1", "a2", "a3"},
		[]string{"a2", "a9"},
	)

	if len(got) != 2 || got[0] != "a1" || got[1] != "a3" {
		t.Fatalf("unexpected filtered ids: %#v", got)
	}
}

func TestIntersectArticleIDsDedupes(t *testing.T) {
	got := intersectArticleIDs(
		[]string{"a1", "a2", "a2", "a3"},
		[]string{"a2", "a4"},
	)

	if len(got) != 1 || got[0] != "a2" {
		t.Fatalf("unexpected intersection: %#v", got)
	}
}
