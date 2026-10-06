package handler

import (
	"context"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/agentx"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/graph"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	recommend "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/engine"
	search "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/engine"
	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type meteredModel struct {
	model.Model
	calls atomic.Int64
}

func (m *meteredModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	return m.Model.GenerateContent(ctx, request)
}

func TestRealModelsContentSearchPlanAndCitation(t *testing.T) {
	if os.Getenv("BTW_TEST_DATACENTER_URL") == "" || os.Getenv("DATACENTER_WORKLOAD_TOKEN_FILE") == "" || os.Getenv("BTW_TEST_POSTGRES_DSN") == "" || os.Getenv("BTW_TEST_ELASTICSEARCH_URL") == "" || os.Getenv("BTW_TEST_NEO4J_URI") == "" {
		t.Skip("requires isolated DataCenter, PostgreSQL, Elasticsearch and Neo4j")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	schema := "btwlive_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	db, e := database.Open(ctx, database.Config{DSN: os.Getenv("BTW_TEST_POSTGRES_DSN"), Schema: schema})
	if e != nil {
		t.Fatal(e)
	}
	sql, _ := db.DB()
	defer sql.Close()
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	objects, e := blob.NewLocal(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	connector, e := datacenter.New(os.Getenv("BTW_TEST_DATACENTER_URL"), datacenter.DeploymentIdentity())
	if e != nil {
		t.Fatal(e)
	}
	manifest, e := connector.Manifest(ctx)
	if e != nil {
		t.Fatal(e)
	}
	encoder := connector.Inference()
	c := agentx.Config{MaxLLMCalls: 4, MaxToolCalls: 3, MaxOutputTokens: 2048}
	baseModel, e := connector.Chat(manifest.Chat.Agent, "agent")
	if e != nil {
		t.Fatal(e)
	}
	meter := &meteredModel{Model: baseModel}
	var m model.Model = meter
	analyzer, closeAnalyzer, e := content.NewAnalyzer(m, c, 2048)
	if e != nil {
		t.Fatal(e)
	}
	defer closeAnalyzer()
	index := idx.NewExact()
	lexical := &elastic.Client{URL: os.Getenv("BTW_TEST_ELASTICSEARCH_URL"), Index: "btwtest_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Username: os.Getenv("BTW_TEST_ELASTICSEARCH_USER"), Password: os.Getenv("BTW_TEST_ELASTICSEARCH_PASSWORD")}
	defer func() {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, lexical.URL+"/"+lexical.Index, nil)
		request.SetBasicAuth(lexical.Username, lexical.Password)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
	graphDriver, e := neo4j.NewDriver(os.Getenv("BTW_TEST_NEO4J_URI"), neo4j.BasicAuth(os.Getenv("BTW_TEST_NEO4J_USER"), os.Getenv("BTW_TEST_NEO4J_PASSWORD"), ""))
	if e != nil {
		t.Fatal(e)
	}
	defer graphDriver.Close(ctx)
	contentGraph := &graph.Client{Driver: graphDriver, Database: os.Getenv("BTW_TEST_NEO4J_DATABASE")}
	retrieval := manifest.Retrieval
	catalog, e := content.New(db, objects, encoder, index, analyzer, content.Config{Retrieval: retrieval, Lexical: lexical, Graph: contentGraph, MaxLexicalSectionBytes: 1 << 20, MaxUploadBytes: 1 << 20, MaxUncompressedBytes: 1 << 20, WindowRunes: 2048, LeaseSeconds: 60, MaxAttempts: 2, UnderstandingModel: manifest.Chat.ContentIdentity})
	if e != nil {
		t.Fatal(e)
	}
	if e = catalog.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = encoder.Ready(ctx, retrieval); e != nil {
		t.Fatal("real encoder is not ready")
	}
	if e = catalog.Prepare(ctx); e != nil {
		t.Fatal(e)
	}
	source := "鞘翅目属于昆虫纲，其前翅硬化，常用于保护后翅和腹部。入门观察时，先阅读昆虫分类资料，再观察前翅形态并记录，最后核对分类依据。观察时保持距离，不捕捉或伤害昆虫。"
	admission, e := catalog.Accept(ctx, item.ContentEvent{Producer: "live-test", EventID: "e1", SourceID: "insects", Revision: 1, Operation: "upsert", Kind: "article", Title: "昆虫入门观察", MediaType: "text/plain", Body: source, OccurredAt: time.Now().UTC()}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer contentGraph.DeleteRoot(context.Background(), admission.ItemID)
	if _, e = catalog.RunOnce(ctx); e != nil {
		t.Fatal("real content processing failed:", e)
	}
	job, e := catalog.Job(ctx, admission.JobID)
	if e != nil || job.State != "done" {
		t.Fatal("real processing did not publish")
	}
	scenario := item.Scenario{Kinds: []string{"article"}, Delivery: "plan", CandidateLimit: 20, Limit: 3, RRF: 60, TimeoutSeconds: 90, MaxEvidenceBytes: 6000, Prompt: "只依据检索资料组织入门学习计划。"}
	scenarios := map[string]item.Scenario{"plan": scenario}
	scenario.Delivery = "search"
	scenarios["search"] = scenario
	scenario.Delivery = "list"
	scenarios["website"] = scenario
	engine, e := search.New(catalog, encoder, index, nil, scenarios, m, c, retrieval, lexical, contentGraph)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	rec := recommend.New(engine, catalog)
	request := item.Request{RequestID: "live-request", Scenario: "website", Ontology: &item.Ontology{UserID: "validation-user", Nodes: []item.Node{{ID: "interest", Type: "interest", Label: "昆虫"}}}, Query: "鞘翅目入门观察", Limit: 2}
	list, e := rec.Recommend(ctx, request, nil)
	if e != nil || len(list.Hits) == 0 {
		t.Fatal("real encoded content did not retrieve")
	}
	request.Scenario = "search"
	for _, level := range []string{"fast", "balanced", "deep"} {
		request.SearchLevel = level
		found, err := engine.Search(ctx, request)
		if err != nil || len(found.Hits) == 0 || len(found.Citations) == 0 {
			t.Fatalf("%s physical search failed: result=%+v error=%v", level, found, err)
		}
		for _, citation := range found.Citations {
			original, readErr := catalog.Read(ctx, citation.Location, 8192)
			if readErr != nil || original != citation.Quote {
				t.Fatalf("%s citation cannot be read back: %v", level, readErr)
			}
		}
		t.Logf("%s physical search: status=%s hits=%d citations=%d path=%s", level, found.Status, len(found.Hits), len(found.Citations), strings.Join(found.Path, ","))
	}
	if benchmarkMode := os.Getenv("BTW_BENCHMARK"); benchmarkMode == "1" || benchmarkMode == "deep" {
		trials := []struct {
			level string
			count int
		}{{"fast", 20}, {"balanced", 5}, {"deep", 3}}
		if benchmarkMode == "deep" {
			trials = []struct {
				level string
				count int
			}{{"deep", 10}}
		}
		for _, trial := range trials {
			durations := make([]float64, 0, trial.count)
			statuses := map[string]int{}
			modelCalls := int64(0)
			retrievalExpressions := 0
			maxRounds := 0
			request.SearchLevel = trial.level
			for i := 0; i < trial.count; i++ {
				request.RequestID = fmt.Sprintf("bench-%s-%d", trial.level, i)
				beforeCalls := meter.calls.Load()
				begin := time.Now()
				found, searchErr := engine.Search(ctx, request)
				durations = append(durations, time.Since(begin).Seconds()*1000)
				modelCalls += meter.calls.Load() - beforeCalls
				if searchErr != nil || len(found.Citations) == 0 {
					t.Fatalf("%s benchmark search failed: %+v %v", trial.level, found, searchErr)
				}
				statuses[found.Status]++
				rounds := 0
				for _, path := range found.Path {
					if path == "elastic" {
						retrievalExpressions++
					}
					if strings.HasPrefix(path, "round:") {
						rounds++
					}
				}
				maxRounds = max(maxRounds, rounds)
				if found.Status != "ok" {
					t.Logf("BENCHMARK detail level=%s trial=%d status=%s warnings=%v path=%v", trial.level, i, found.Status, found.Warnings, found.Path)
				}
			}
			sort.Float64s(durations)
			t.Logf("BENCHMARK level=%s samples=%d p50_ms=%.2f p95_ms=%.2f observed_max_ms=%.2f llm_calls=%d retrieval_expressions=%d max_rounds=%d statuses=%v corpus_items=1", trial.level, trial.count, durations[(trial.count-1)/2], durations[min(trial.count-1, int(math.Ceil(.95*float64(trial.count)))-1)], durations[trial.count-1], modelCalls, retrievalExpressions, maxRounds, statuses)
		}
	}
	request.Scenario = "plan"
	request.SearchLevel = ""
	var planDraft strings.Builder
	plan, e := rec.Recommend(ctx, request, func(event item.StreamEvent) error {
		if event.Type == "delta" {
			planDraft.WriteString(event.Delta)
		}
		return nil
	})
	if e != nil {
		draft := []rune(planDraft.String())
		t.Fatalf("real plan generation failed: %v; model delta prefix=%q", e, string(draft[:min(120, len(draft))]))
	}
	if plan.Plan == nil || len(plan.Citations) == 0 {
		t.Fatal("real model did not produce a grounded plan")
	}
	for _, citation := range plan.Citations {
		text, e := catalog.Read(ctx, citation.Location, 8192)
		if e != nil || text != citation.Quote {
			t.Fatal("citation cannot be read back")
		}
	}
	request.Scenario = "search"
	request.SearchLevel = ""
	answer, e := engine.Generate(ctx, request, false, nil)
	if e != nil || answer.Answer == "" || len(answer.Citations) == 0 {
		t.Fatal("real answer did not complete")
	}
	t.Logf("real BGE + configured LLM + PostgreSQL + Elasticsearch + Neo4j + saved original passed; item count=%d, plan citations=%d, answer citations=%d; vector index was the explicit Exact numerical reference, not Milvus", len(list.Hits), len(plan.Citations), len(answer.Citations))
}
