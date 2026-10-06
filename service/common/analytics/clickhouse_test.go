package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPhysicalFeedbackLabels(t *testing.T) {
	url := os.Getenv("BTW_TEST_CLICKHOUSE_URL")
	if url == "" {
		t.Skip("requires isolated ClickHouse")
	}
	ctx := context.Background()
	client := &ClickHouse{URL: url, Database: "btwtest_" + strings.ReplaceAll(uuid.NewString(), "-", ""), User: os.Getenv("BTW_TEST_CLICKHOUSE_USER"), Password: os.Getenv("BTW_TEST_CLICKHOUSE_PASSWORD")}
	if e := client.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	defer client.query(ctx, "DROP DATABASE "+client.Database, nil)
	at := time.Now().UTC().Add(-48 * time.Hour)
	candidate := item.Behavior{EventID: "c1", RequestID: "q1", UserID: "u", ItemID: "a", RootID: "a", DuplicateID: "cluster", SourceRevision: "r1", Scenario: "website", Type: "candidate", OccurredAt: at, Features: []float64{1, 1, 1, 1, 1, 0, 1}}
	impression := candidate
	impression.EventID = "i1"
	impression.Type = "impression"
	click := candidate
	click.EventID = "click1"
	click.Type = "click"
	unexposed := candidate
	unexposed.EventID = "c2"
	unexposed.ItemID = "b"
	if e := client.Write(ctx, []item.Behavior{candidate, impression, click, click, unexposed}); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e := client.ExportTraining(ctx, []string{"website"}, time.Now().UTC(), 24*time.Hour, &out); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("unexposed candidate or replay became training label: %s", out.String())
	}
	var row map[string]any
	if e := json.Unmarshal([]byte(lines[0]), &row); e != nil {
		t.Fatal(e)
	}
	if row["label_source"] != "behavior" || row["label"] != float64(2) {
		t.Fatalf("wrong label %+v", row)
	}
}
