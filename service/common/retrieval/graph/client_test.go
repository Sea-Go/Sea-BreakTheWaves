package graph

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

func TestContentConceptKey(t *testing.T) {
	if Normalize(" FACT ", " 鞘翅目 ") != "fact:鞘翅目" {
		t.Fatal("concept key changed")
	}
}
func TestIsolatedGraphPathAndDeletion(t *testing.T) {
	uri := os.Getenv("BTW_TEST_NEO4J_URI")
	if uri == "" {
		t.Skip("requires isolated Neo4j")
	}
	ctx := context.Background()
	driver, err := neo4j.NewDriver(uri, neo4j.BasicAuth(os.Getenv("BTW_TEST_NEO4J_USER"), os.Getenv("BTW_TEST_NEO4J_PASSWORD"), ""))
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close(ctx)
	if err = driver.VerifyConnectivity(ctx); err != nil {
		t.Fatal(err)
	}
	client := &Client{Driver: driver, Database: os.Getenv("BTW_TEST_NEO4J_DATABASE")}
	if err = client.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	rootID := "book-" + suffix
	factID := "fact-" + suffix
	otherID := "other-" + suffix
	otherTitle := "后翅" + suffix
	revision := "source-" + suffix
	title := "鞘翅目" + suffix
	defer func() {
		_ = client.DeleteRoot(ctx, rootID)
		_, _ = client.execute(ctx, `MATCH (c:ContentConcept) WHERE c.key IN $keys DELETE c`, map[string]any{"keys": []string{Normalize("fact", title), Normalize("fact", otherTitle)}}, false)
	}()
	root := item.Item{ID: rootID, RootID: rootID, RootKind: "book", Kind: "book", RevisionID: revision, Title: "book"}
	fact := item.Item{ID: factID, RootID: rootID, RootKind: "book", Kind: "fact", RevisionID: revision, Title: title, Location: item.Location{RevisionID: revision, SectionID: "chapter", Start: 10, End: 20}}
	other := item.Item{ID: otherID, RootID: rootID, RootKind: "book", Kind: "fact", RevisionID: revision, Title: otherTitle, Location: item.Location{RevisionID: revision, SectionID: "later", Start: 30, End: 40}}
	if err = client.Upsert(ctx, []item.Item{root, fact, other}, nil); err != nil {
		t.Fatal(err)
	}
	hits, err := client.Search(ctx, []string{title}, []string{"book"}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ItemID != factID || hits[0].Location.SectionID != "chapter" {
		t.Fatalf("grounded one-hop: %+v", hits)
	}
	two, err := client.Search(ctx, []string{title}, []string{"book"}, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundRoot := false
	for _, hit := range two {
		if hit.ItemID == rootID && hit.Hops == 2 {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Fatalf("two-hop root missing: %+v", two)
	}
	three, err := client.Search(ctx, []string{title}, []string{"book"}, 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundOther := false
	for _, hit := range three {
		if hit.ItemID == otherID {
			foundOther = hit.Location.SectionID == "later" && hit.Location.Start == 30
		}
	}
	if !foundOther {
		t.Fatalf("three-hop evidence points to wrong object: %+v", three)
	}
	if err = client.DeleteRoot(ctx, rootID); err != nil {
		t.Fatal(err)
	}
	hits, err = client.Search(ctx, []string{title}, []string{"book"}, 1, 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted graph visible: %+v %v", hits, err)
	}
}
