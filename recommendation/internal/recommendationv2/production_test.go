package recommendationv2

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"sea/storage"
)

func TestProductionProvidersUseStorageBackedDomainPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New returned error: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT article_id FROM articles")).
		WithArgs(50).
		WillReturnRows(sqlmock.NewRows([]string{"article_id"}).
			AddRow("art_hot_1").
			AddRow("art_hot_2"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT article_id, title, cover, type_tags, tags, score FROM articles WHERE article_id IN ($1,$2)")).
		WithArgs("art_hot_1", "art_hot_2").
		WillReturnRows(sqlmock.NewRows([]string{"article_id", "title", "cover", "type_tags", "tags", "score"}).
			AddRow("art_hot_1", "Hot One", "cover-1", "news,ai", "go,trpc", 0.90).
			AddRow("art_hot_2", "Hot Two", "cover-2", "news", "agent", 0.70))

	rt := NewRecommendationRuntime(WithProductionProviders(storage.NewArticleRepo(db), nil))
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-prod",
		Channel:  "home",
		User:     UserIdentity{UserID: "u-prod"},
		TopK:     2,
		PathMode: PathHybrid,
		Debug:    true,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected storage-backed items, got %d: %+v", len(resp.Items), resp.Items)
	}
	if resp.Items[0].ArticleID != "art_hot_1" {
		t.Fatalf("expected highest scored storage article first, got %+v", resp.Items[0])
	}
	if resp.Items[0].Features["title"] != "Hot One" {
		t.Fatalf("storage metadata not propagated into features: %+v", resp.Items[0].Features)
	}
	if resp.Items[0].Features["rerank_model"] == nil {
		t.Fatalf("production rerank metadata missing: %+v", resp.Items[0].Features)
	}
	if resp.Items[0].Features["quality_score"] != productionDefaultQualityScore {
		t.Fatalf("production quality metadata missing: %+v", resp.Items[0].Features)
	}
	if resp.Cost.ToolCalls < 3 {
		t.Fatalf("expected recall/rank/rerank tool calls, got %+v", resp.Cost)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
