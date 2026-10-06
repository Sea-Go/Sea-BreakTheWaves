package search

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/agentx"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/graph"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type readerFixture struct {
	retrieval item.Retrieval
	values    map[string]item.Item
}

func (r *readerFixture) Resolve(_ context.Context, ids, kinds []string) (map[string]item.Item, error) {
	out := map[string]item.Item{}
	for _, id := range ids {
		if v, ok := r.values[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}
func (r *readerFixture) Read(ctx context.Context, loc item.Location, _ int64) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	return "alpha", nil
}

type encoderFixture struct{}

type lexicalFixture struct{}

func (lexicalFixture) Search(_ context.Context, q elastic.Query) ([]elastic.Hit, error) {
	first, second := "a", "b"
	if strings.Contains(q.Text, "beta") {
		first, second = second, first
	}
	return []elastic.Hit{{ItemID: first, RevisionID: "rev-" + first, SectionID: "s", End: 5, Score: 2}, {ItemID: second, RevisionID: "rev-" + second, SectionID: "s", End: 5, Score: 1}}, nil
}

type graphFixture struct{}

func (graphFixture) Search(context.Context, []string, []string, int, int) ([]graph.Candidate, error) {
	return []graph.Candidate{}, nil
}

func (encoderFixture) Encode(_ context.Context, model, space string, texts []string) ([]inference.Encoding, error) {
	out := []inference.Encoding{}
	for _, text := range texts {
		dense := []float32{1, 0}
		sparse := inference.Sparse{Indices: []uint32{1}, Values: []float32{1}}
		if strings.Contains(text, "beta") {
			dense = []float32{0, 1}
			sparse.Indices = []uint32{2}
		}
		out = append(out, inference.Encoding{Model: model, Space: space, Segments: []inference.Segment{{Start: 0, End: int64(len([]rune(text))), Dense: dense, Sparse: sparse, Multi: [][]float32{dense}}}})
	}
	return out, nil
}

type toolModel struct {
	calls  atomic.Int32
	fail   bool
	invent bool
}

func (m *toolModel) Info() model.Info { return model.Info{Name: "fixture-tools"} }
func (m *toolModel) GenerateContent(ctx context.Context, req *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	if m.fail {
		return nil, errors.New("provider failed")
	}
	out := make(chan *model.Response, 1)
	defer close(out)
	var citation item.Citation
	found := false
	for _, msg := range req.Messages {
		if msg.Role == model.RoleTool {
			if json.Unmarshal([]byte(msg.Content), &citation) == nil && citation.ID != "" {
				found = true
			}
		}
	}
	message := model.Message{Role: model.RoleAssistant}
	if !found {
		message.ToolCalls = []model.ToolCall{{ID: "read-1", Type: "function", Function: model.FunctionDefinitionParam{Name: "read_source", Arguments: []byte(`{"item_id":"a","section_id":"s","start":0,"end":5}`)}}}
	} else {
		if m.invent {
			citation.ID = "invented"
		}
		value := map[string]any{"status": "ok", "answer": "alpha", "citation_ids": []string{citation.ID}, "plan": map[string]any{"name": "test plan", "goal": "learn alpha", "steps": []map[string]any{{"action": "read alpha", "citation_ids": []string{citation.ID}}}}}
		data, _ := json.Marshal(value)
		message.Content = string(data)
	}
	out <- &model.Response{Object: model.ObjectTypeChatCompletion, Done: found, Choices: []model.Choice{{Message: message}}}
	return out, nil
}
func fixtureSearch(t *testing.T, m model.Model) (*Service, *readerFixture) {
	t.Helper()
	retrieval := item.Retrieval{Model: "fixture", Space: "query", Dimensions: 2, Dense: "d", Sparse: "s", Multi: "m", ItemTower: "item-projection", UserSpace: "users", MatchDimensions: 2, Match: "u"}
	reader := &readerFixture{retrieval: retrieval, values: map[string]item.Item{}}
	index := idx.NewExact()
	index.Ensure(context.Background(), retrieval)
	for _, v := range []struct {
		id, kind string
		vector   []float32
		token    uint32
	}{{"a", "article", []float32{1, 0}, 1}, {"b", "article", []float32{0, 1}, 2}, {"book", "book", []float32{10, 10}, 1}, {"book-child", "book", []float32{20, 20}, 1}} {
		loc := item.Location{RevisionID: "rev-" + v.id, SectionID: "s", Start: 0, End: 5}
		reader.values[v.id] = item.Item{ID: v.id, RootID: v.id, RevisionID: loc.RevisionID, Kind: v.kind, RootKind: v.kind, Location: loc}
		index.Upsert(context.Background(), retrieval, []idx.Record{{ID: v.id, ItemID: v.id, RootID: v.id, RootKind: v.kind, RevisionID: loc.RevisionID, Location: loc, Match: v.vector, Encoding: inference.Segment{Dense: v.vector, Sparse: inference.Sparse{Indices: []uint32{v.token}, Values: []float32{v.vector[0] + v.vector[1]}}, Multi: [][]float32{v.vector}}}})
	}
	cfg := item.Scenario{Kinds: []string{"article"}, Delivery: "search", CandidateLimit: 20, Limit: 10, RRF: 60, TimeoutSeconds: 3, MaxEvidenceBytes: 4096}
	scenarios := map[string]item.Scenario{"search": cfg}
	cfg.Delivery = "list"
	scenarios["website"] = cfg
	cfg.Delivery = "plan"
	scenarios["plan"] = cfg
	s, e := New(reader, encoderFixture{}, index, nil, scenarios, m, agentx.Config{MaxLLMCalls: 5, MaxToolCalls: 3}, retrieval, lexicalFixture{}, graphFixture{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, reader
}
func request(scenario, label string) item.Request {
	return item.Request{RequestID: "request-1", Scenario: scenario, Ontology: &item.Ontology{UserID: "user", Nodes: []item.Node{{ID: "interest", Type: "interest", Label: label}}}, Limit: 2}
}
func TestOntologyChangesActualRetrievalAndWebsiteScope(t *testing.T) {
	m := &toolModel{}
	s, _ := fixtureSearch(t, m)
	a, e := s.Search(context.Background(), request("website", "alpha"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Search(context.Background(), request("website", "beta"))
	if e != nil {
		t.Fatal(e)
	}
	if a.Hits[0].Item.ID != "a" || b.Hits[0].Item.ID != "b" {
		t.Fatalf("ontology did not change retrieval: %+v %+v", a.Hits, b.Hits)
	}
	for _, h := range append(a.Hits, b.Hits...) {
		if h.Item.RootKind != "article" {
			t.Fatal("book candidate escaped pre-retrieval scope")
		}
	}
	if m.calls.Load() != 0 {
		t.Fatal("list used generation model")
	}
}
func TestExternalVectorSpacePaths(t *testing.T) {
	s, _ := fixtureSearch(t, nil)
	req := request("website", "alpha")
	req.Ontology.Vector = &item.UserVector{Space: "other", Values: []float32{0, 1}}
	out, e := s.Search(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Warnings) == 0 || out.Hits[0].Item.ID != "a" {
		t.Fatal("incompatible vector did not use graph path")
	}
	req.Ontology.Nodes = nil
	req.Ontology.Vector.Space = "users"
	out, e = s.Search(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if out.Hits[0].Item.ID != "b" || !strings.Contains(strings.Join(out.Path, ","), "external_fixed_user_vector") {
		t.Fatalf("matching vector not used: %+v", out)
	}
}

func TestMissingHostGenerationModelIsExplicit(t *testing.T) {
	s, _ := fixtureSearch(t, nil)
	req := request("search", "alpha")
	req.Query = "alpha"
	req.SearchLevel = "fast"
	fast, err := s.Search(context.Background(), req)
	if err != nil || fast.Status != "ok" || len(fast.Hits) == 0 {
		t.Fatalf("fast should stay model-free: %+v %v", fast, err)
	}
	req.SearchLevel = "balanced"
	balanced, err := s.Search(context.Background(), req)
	if err != nil || balanced.Status != "degraded" || len(balanced.Hits) == 0 || !strings.Contains(strings.Join(balanced.Warnings, ","), "query_planning_failed") {
		t.Fatalf("missing planner was hidden: %+v %v", balanced, err)
	}
	req.SearchLevel = "deep"
	deep, err := s.Search(context.Background(), req)
	if err != nil || deep.Status != "degraded" || !strings.Contains(strings.Join(deep.Warnings, ","), "evidence_assessment_failed") {
		t.Fatalf("missing assessor was hidden: %+v %v", deep, err)
	}
	req = request("plan", "alpha")
	if _, err = s.Generate(context.Background(), req, true, nil); !errors.Is(err, item.ErrUnavailable) {
		t.Fatalf("plan without injected generation model succeeded: %v", err)
	}
}
func TestFrameworkToolEvidenceAndStructuredPlan(t *testing.T) {
	m := &toolModel{}
	s, _ := fixtureSearch(t, m)
	out, e := s.Generate(context.Background(), request("plan", "alpha"), true, nil)
	if e != nil {
		t.Fatal(e)
	}
	if m.calls.Load() < 2 || out.Plan == nil || len(out.Citations) == 0 || out.Citations[0].Quote != "alpha" {
		t.Fatalf("model/tool path incomplete: %+v", out)
	}
}
func TestProviderFailureAndInventedCitationStayFailures(t *testing.T) {
	for _, m := range []*toolModel{{fail: true}, {invent: true}} {
		s, _ := fixtureSearch(t, m)
		if _, e := s.Generate(context.Background(), request("search", "alpha"), false, nil); e == nil {
			t.Fatal("model failure converted to success")
		}
	}
}
func TestCancellation(t *testing.T) {
	s, _ := fixtureSearch(t, &toolModel{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Generate(ctx, request("search", "alpha"), false, nil); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel error: %v", e)
	}
}

// Only ranker inference is live; candidate inputs stay fixed to isolate ordering.
func TestRealRankerChangesFixedCandidateOrdering(t *testing.T) {
	endpoint := os.Getenv("BTW_TEST_DATACENTER_URL")
	if endpoint == "" || os.Getenv("DATACENTER_WORKLOAD_TOKEN_FILE") == "" {
		t.Skip("requires DataCenter with an active trained ranker")
	}
	connector, err := datacenter.New(endpoint, datacenter.DeploymentIdentity())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := connector.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Retrieval.SearchRanker == "" {
		t.Skip("DataCenter has no active search ranker")
	}
	s, reader := fixtureSearch(t, nil)
	a, b := reader.values["a"], reader.values["b"]
	a.Features = map[string]float64{"quality": 0}
	b.Features = map[string]float64{"quality": 1}
	reader.values["a"], reader.values["b"] = a, b
	req := request("search", "alpha")
	baseline, err := s.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	reader.retrieval.SearchRanker = manifest.Retrieval.SearchRanker
	s.Ranker = connector.Inference()
	ranked, err := s.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Hits) != 2 || len(ranked.Hits) != 2 || baseline.Hits[0].Item.ID != "a" || ranked.Hits[0].Item.ID != "b" {
		t.Fatalf("ranker did not change fixed candidates: %+v %+v", baseline, ranked)
	}
	t.Log("real LightGBM HTTP model changed Search order from a,b to b,a with the same candidate set")
}

type recordedLexical struct {
	mu    sync.Mutex
	calls []string
}

func (l *recordedLexical) Search(ctx context.Context, q elastic.Query) ([]elastic.Hit, error) {
	l.mu.Lock()
	l.calls = append(l.calls, q.Text)
	l.mu.Unlock()
	if q.Text == "missing" {
		return nil, nil
	}
	return lexicalFixture{}.Search(ctx, q)
}

type recordedGraph struct {
	mu     sync.Mutex
	depths []int
}

func (g *recordedGraph) Search(_ context.Context, _ []string, _ []string, depth, _ int) ([]graph.Candidate, error) {
	g.mu.Lock()
	g.depths = append(g.depths, depth)
	g.mu.Unlock()
	return nil, nil
}

type recordedEncoder struct {
	mu    sync.Mutex
	calls int
}

func (e *recordedEncoder) Encode(ctx context.Context, model, space string, texts []string) ([]inference.Encoding, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	values, err := encoderFixture{}.Encode(ctx, model, space, texts)
	if len(texts) > 0 && texts[0] == "missing" {
		values[0].Space = "empty"
	}
	return values, err
}

type recordedIndex struct {
	idx.Index
	mu    sync.Mutex
	calls []string
}

func (i *recordedIndex) Search(ctx context.Context, r item.Retrieval, lane string, q inference.Encoding, kinds []string, k int) ([]idx.Candidate, error) {
	i.mu.Lock()
	i.calls = append(i.calls, lane)
	i.mu.Unlock()
	if q.Space == "empty" {
		return nil, nil
	}
	return i.Index.Search(ctx, r, lane, q, kinds, k)
}

type planningModel struct{ calls atomic.Int32 }

func (m *planningModel) Info() model.Info { return model.Info{Name: "planner-fixture"} }
func (m *planningModel) GenerateContent(_ context.Context, request *model.Request) (<-chan *model.Response, error) {
	call := m.calls.Add(1)
	value := `{"queries":["beta"]}`
	if call == 1 {
		value = `{"queries":["missing"]}`
	}
	for _, message := range request.Messages {
		if !strings.Contains(message.Content, "candidate_citations") {
			continue
		}
		matches := regexp.MustCompile(`"id":"([^"]+)"`).FindAllStringSubmatch(message.Content, -1)
		supported := []map[string]any{}
		if len(matches) > 0 {
			supported = append(supported, map[string]any{"query_index": 0, "citation_ids": []string{matches[0][1]}})
		}
		if call >= 4 && len(matches) > 1 {
			supported = append(supported, map[string]any{"query_index": 1, "citation_ids": []string{matches[1][1]}})
		}
		raw, _ := json.Marshal(map[string]any{"supported": supported})
		value = string(raw)
		break
	}
	out := make(chan *model.Response, 1)
	out <- &model.Response{Object: model.ObjectTypeChatCompletion, Done: true, Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant, Content: value}}}}
	close(out)
	return out, nil
}
func (r *readerFixture) ExactMulti(_ context.Context, _ item.Item, _ item.Location, _ [][]float32) (float64, error) {
	return .7, nil
}

func TestSearchLevelsExecuteDifferentWork(t *testing.T) {
	t.Run("fast", func(t *testing.T) {
		s, _ := fixtureSearch(t, nil)
		lex := &recordedLexical{}
		g := &recordedGraph{}
		enc := &recordedEncoder{}
		i := &recordedIndex{Index: s.Index}
		s.Lexical = lex
		s.Graph = g
		s.Encoder = enc
		s.Index = i
		req := request("search", "alpha")
		req.Query = "alpha"
		req.SearchLevel = "fast"
		out, err := s.Search(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Hits) == 0 || len(out.Citations) == 0 || len(lex.calls) != 1 || len(g.depths) != 1 || g.depths[0] != 1 || enc.calls != 0 || len(i.calls) != 0 {
			t.Fatalf("fast did unexpected work: %+v lexical=%v graph=%v encode=%d vectors=%v", out, lex.calls, g.depths, enc.calls, i.calls)
		}
	})
	t.Run("balanced", func(t *testing.T) {
		planner := &planningModel{}
		s, _ := fixtureSearch(t, planner)
		lex := &recordedLexical{}
		g := &recordedGraph{}
		enc := &recordedEncoder{}
		i := &recordedIndex{Index: s.Index}
		s.Lexical = lex
		s.Graph = g
		s.Encoder = enc
		s.Index = i
		req := request("search", "alpha")
		req.Query = "alpha"
		req.SearchLevel = "balanced"
		out, err := s.Search(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if planner.calls.Load() != 1 || len(lex.calls) != 2 || enc.calls != 2 || len(i.calls) != 4 || len(g.depths) != 2 || g.depths[0] != 2 || out.Status != "ok" {
			t.Fatalf("balanced depth lost: %+v plan=%d lexical=%v graph=%v encode=%d vectors=%v", out, planner.calls.Load(), lex.calls, g.depths, enc.calls, i.calls)
		}
		for _, lane := range i.calls {
			if lane == "multi" {
				t.Fatal("balanced used multi-vector")
			}
		}
	})
	t.Run("deep", func(t *testing.T) {
		planner := &planningModel{}
		s, _ := fixtureSearch(t, planner)
		lex := &recordedLexical{}
		g := &recordedGraph{}
		enc := &recordedEncoder{}
		i := &recordedIndex{Index: s.Index}
		s.Lexical = lex
		s.Graph = g
		s.Encoder = enc
		s.Index = i
		req := request("search", "alpha")
		req.Query = "alpha"
		req.SearchLevel = "deep"
		out, err := s.Search(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if planner.calls.Load() != 4 || len(lex.calls) != 3 || enc.calls != 3 || len(i.calls) != 9 || len(g.depths) != 3 || g.depths[0] != 3 || out.Status != "ok" {
			t.Fatalf("deep depth lost: %+v plan=%d lexical=%v graph=%v encode=%d vectors=%v", out, planner.calls.Load(), lex.calls, g.depths, enc.calls, i.calls)
		}
		if !strings.Contains(strings.Join(out.Path, ","), "multi_exact_bounded") || !strings.Contains(strings.Join(out.Path, ","), "round:2") {
			t.Fatal("deep did not verify evidence in second round")
		}
	})
}
