package elastic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNativeFullTextRequestAndItemProvenance(t *testing.T) {
	var indexed bool
	var searched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "HEAD":
			w.WriteHeader(404)
		case r.Method == "PUT" && r.URL.Path == "/btw_test":
			indexed = true
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/btw_test/_mapping":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"btw_test":{"mappings":{"properties":{"body":{"type":"text","analyzer":"cjk","index_options":"offsets"},"root_kind":{"type":"keyword"},"item_id":{"type":"keyword"},"title":{"fields":{"exact":{"type":"keyword"}}}}}}}`)
		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/_doc/"):
			raw, _ := io.ReadAll(r.Body)
			var doc Document
			if err := json.Unmarshal(raw, &doc); err != nil || doc.ItemID != "fact" || doc.RevisionID != "source-current" {
				t.Errorf("wrong indexed source: %v %+v", err, doc)
			}
			w.WriteHeader(201)
		case strings.Contains(r.URL.Path, "/_search"):
			var query map[string]any
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Error(err)
			}
			if _, ok := query["collapse"]; !ok {
				t.Error("missing item collapse")
			}
			boolQuery := query["query"].(map[string]any)["bool"].(map[string]any)
			if len(boolQuery["filter"].([]any)) == 0 || len(boolQuery["should"].([]any)) < 3 {
				t.Error("missing scope or phrase lane")
			}
			searched = true
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"hits":{"hits":[{"_score":2.5,"_source":{"item_id":"fact","revision_id":"source-current","section_id":"chapter","start":12,"end":22,"title":"鞘翅目"},"highlight":{"body":["<em>鞘翅目</em>保护后翅"]}}]}}`)
		case strings.Contains(r.URL.Path, "/_delete_by_query"):
			w.WriteHeader(200)
		case strings.Contains(r.URL.Path, "/_refresh"):
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := &Client{URL: server.URL, Index: "btw_test"}
	ctx := context.Background()
	if err := client.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Upsert(ctx, Document{ID: "row", ItemID: "fact", RootID: "book", RootKind: "book", RevisionID: "source-current", SectionID: "chapter", Start: 12, End: 22, Body: "鞘翅目保护后翅"}); err != nil {
		t.Fatal(err)
	}
	hits, err := client.Search(ctx, Query{Text: "鞘翅目", Phrases: []string{"保护后翅"}, Kinds: []string{"book"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ItemID != "fact" || hits[0].SectionID != "chapter" || hits[0].Highlight == "" {
		t.Fatalf("lost provenance: %+v", hits)
	}
	if err = client.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = client.DeleteRoot(ctx, "book"); err != nil {
		t.Fatal(err)
	}
	if !indexed || !searched {
		t.Fatal("backend path not executed")
	}
}
func TestIsolatedElasticsearchPhysicalContract(t *testing.T) {
	address := os.Getenv("BTW_TEST_ELASTICSEARCH_URL")
	if address == "" {
		t.Skip("requires isolated Elasticsearch")
	}
	name := "btwtest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	client := &Client{URL: address, Index: name, Username: os.Getenv("BTW_TEST_ELASTICSEARCH_USER"), Password: os.Getenv("BTW_TEST_ELASTICSEARCH_PASSWORD")}
	ctx := context.Background()
	defer func() {
		response, err := client.request(ctx, http.MethodDelete, "", nil)
		if err == nil {
			response.Body.Close()
		}
	}()
	if err := client.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Upsert(ctx, Document{ID: "fact", ItemID: "fact", RootID: "book", RootKind: "book", Kind: "fact", RevisionID: "accepted", SectionID: "one", Start: 0, End: 9, Title: "鞘翅目", Body: "鞘翅目保护后翅", Language: "zh"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := client.Search(ctx, Query{Text: "鞘翅目", Phrases: []string{"保护后翅"}, Kinds: []string{"book"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ItemID != "fact" {
		t.Fatalf("physical lexical result: %+v", hits)
	}
	if err := client.DeleteRoot(ctx, "book"); err != nil {
		t.Fatal(err)
	}
	hits, err = client.Search(ctx, Query{Text: "鞘翅目", Kinds: []string{"book"}, Limit: 10})
	if err != nil || len(hits) != 0 {
		t.Fatalf("physical deletion: %+v %v", hits, err)
	}
}
