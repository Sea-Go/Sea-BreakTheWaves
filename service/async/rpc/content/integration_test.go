package content

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/catalog"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/processing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/graph"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type fixtureModels struct{ understandCalls int }

func (f *fixtureModels) Understand(_ context.Context, title, text string) (processing.Understanding, error) {
	f.understandCalls++
	quote := string([]rune(text)[:min(len([]rune(text)), 20)])
	return processing.Understanding{Summary: title + ": " + quote, Topics: []string{"fixture"}, Objects: []processing.Object{{Kind: "fact", Title: title, Summary: quote, Quote: quote}}, Queries: []processing.Query{{Text: "what is " + title, Quote: quote, Relevance: 3}}}, nil
}
func (f *fixtureModels) Summarize(_ context.Context, parts []string) (string, error) {
	return strings.Join(parts, " / "), nil
}
func (f *fixtureModels) Encode(_ context.Context, model, space string, texts []string) ([]inference.Encoding, error) {
	out := []inference.Encoding{}
	for _, text := range texts {
		out = append(out, inference.Encoding{Model: model, Space: space, Segments: []inference.Segment{{Start: 0, End: int64(len([]rune(text))), Dense: []float32{1, 2}, Sparse: inference.Sparse{Indices: []uint32{1}, Values: []float32{2}}, Multi: [][]float32{{1, 2}}}}})
	}
	return out, nil
}
func fixture(t *testing.T) (*Service, *fixtureModels) {
	t.Helper()
	dsn := os.Getenv("BTW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	schema := "btwtest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	db, e := database.Open(context.Background(), database.Config{DSN: dsn, Schema: schema, MaxOpenConns: 8})
	if e != nil {
		t.Fatal(e)
	}
	sql, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Exec("DROP SCHEMA " + schema + " CASCADE").Error; sql.Close() })
	objects, e := blob.NewLocal(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	models := &fixtureModels{}
	svc, e := New(db, objects, models, idx.NewExact(), models, Config{Retrieval: item.Retrieval{Model: "fixture", Space: "fixture", Dimensions: 2, Dense: "test_dense", Sparse: "test_sparse", Multi: "test_multi"}, MaxUploadBytes: 64 << 20, MaxUncompressedBytes: 128 << 20, WindowRunes: 1024, LeaseSeconds: 30, MaxAttempts: 3, UnderstandingModel: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	if e = svc.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = svc.Prepare(context.Background()); e != nil {
		t.Fatal(e)
	}
	return svc, models
}
func event(id string, revision int64, body string) item.ContentEvent {
	return item.ContentEvent{Producer: "test", EventID: fmt.Sprintf("%s-%d", id, revision), SourceID: id, Revision: revision, Operation: "upsert", Kind: "article", Title: "title", MediaType: "text/plain", Body: body}
}
func TestDurableRevisionsDeleteAndRecovery(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	input := event("one", 1, "first original")
	a, e := s.Accept(ctx, input, nil)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Accept(ctx, input, nil)
	if e != nil || !again.Duplicate || again.RevisionID != a.RevisionID {
		t.Fatalf("duplicate: %+v %v", again, e)
	}
	conflict := input
	conflict.Body = "changed"
	if _, e = s.Accept(ctx, conflict, nil); !errors.Is(e, item.ErrConflict) {
		t.Fatalf("conflict %v", e)
	}
	if worked, e := s.RunOnce(ctx); !worked || e != nil {
		t.Fatalf("process %v", e)
	}
	v, e := s.Item(ctx, a.ItemID)
	if e != nil || v.RevisionID != a.RevisionID {
		t.Fatalf("item %+v %v", v, e)
	}
	next, e := s.Accept(ctx, event("one", 3, "third original"), nil)
	if e != nil {
		t.Fatal(e)
	}
	v, e = s.Item(ctx, a.ItemID)
	if e != nil || v.RevisionID != a.RevisionID {
		t.Fatal("update hid previous serving revision")
	}
	stale, e := s.Accept(ctx, event("one", 2, "late second original"), nil)
	if e != nil || !stale.Obsolete {
		t.Fatalf("stale: %+v %v", stale, e)
	}
	j, e := s.store.Claim(ctx, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if j.RevisionID != next.RevisionID {
		t.Fatal("wrong claimed revision")
	}
	del := event("one", 4, "")
	del.Operation = "delete"
	if _, e = s.Accept(ctx, del, nil); e != nil {
		t.Fatal(e)
	}
	if e = s.store.Publish(ctx, j); !errors.Is(e, item.ErrStale) {
		t.Fatalf("late worker published %v", e)
	}
	if _, e = s.Item(ctx, a.ItemID); !errors.Is(e, item.ErrNotFound) {
		t.Fatalf("deleted visible: %v", e)
	}
	if e = s.store.Readable(ctx, a.RevisionID); !errors.Is(e, item.ErrNotFound) {
		t.Fatal("deleted source remained public")
	}
	oldFile, _, e := s.File(ctx, a.RevisionID, 0, -1)
	if oldFile != nil {
		oldFile.Close()
	}
	if !errors.Is(e, item.ErrNotFound) {
		t.Fatal("deleted file publicly readable")
	}
	rev, e := s.store.Revision(ctx, a.RevisionID)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.objects.Open(ctx, rev.FileKey, 0, -1)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "first original" {
		t.Fatal("original was lost")
	}
}
func TestWorkerLeaseAndDeploymentCatchup(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	a, e := s.Accept(ctx, event("lease", 1, "retained"), nil)
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.store.Claim(ctx, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	s.store.DB.Model(&catalog.Job{}).Where("id=?", first.ID).Update("lease_until", time.Now().Add(-time.Second))
	second, e := s.store.Claim(ctx, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if first.Token == second.Token {
		t.Fatal("attempt token reused")
	}
	if e = s.store.Stage(ctx, first, "bad"); !errors.Is(e, item.ErrStale) {
		t.Fatal("expired attempt updated job")
	}
	if e = s.process(ctx, second); e != nil {
		t.Fatal(e)
	}
	job, e := s.Job(ctx, a.JobID)
	if e != nil || job.State != "done" {
		t.Fatalf("job %+v %v", job, e)
	}
	before, e := s.BuildIndexes(ctx)
	if e != nil {
		t.Fatal(e)
	}
	del := event("lease", 2, "")
	del.Operation = "delete"
	if _, e = s.Accept(ctx, del, nil); e != nil {
		t.Fatal(e)
	}
	after, e := s.BuildIndexes(ctx)
	if e != nil || after <= before {
		t.Fatalf("index catchup did not observe delete: %d %d %v", before, after, e)
	}
	if _, e = s.Item(ctx, a.ItemID); !errors.Is(e, item.ErrNotFound) {
		t.Fatalf("deleted item visible: %v", e)
	}
}
func TestEPUBStructureAndOriginalLocations(t *testing.T) {
	s, _ := fixture(t)
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	z.Create("OEBPS/")
	members := map[string]string{"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/book.opf"/></rootfiles></container>`, "OEBPS/book.opf": `<package><manifest><item id="a" href="a.xhtml"/><item id="b" href="b.xhtml"/><item id="nav" href="nav.xhtml" properties="nav"/></manifest><spine><itemref idref="b"/><itemref idref="a" linear="no"/></spine></package>`, "OEBPS/nav.xhtml": `<nav><ol><li><a href="b.xhtml">第二章</a><ol><li><a href="b.xhtml#detail">详情</a></li></ol></li><li><a href="a.xhtml">第一章</a></li></ol></nav>`, "OEBPS/a.xhtml": `<html><body><p>第一章保存稀有词：鞘翅目。</p><script>should not run or appear</script></body></html>`, "OEBPS/b.xhtml": `<html><body><p>第二章引用第一章的概念。</p><p id="detail">跨章节说明。</p></body></html>`}
	for name, text := range members {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte(text))
	}
	z.Close()
	e := event("book", 1, "")
	e.Kind = "book"
	e.MediaType = "application/epub+zip"
	a, err := s.Accept(context.Background(), e, bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	revision, sections, err := s.Structure(context.Background(), a.ItemID, 0, 100)
	if err != nil || len(sections) != 2 || sections[0].Title != "第二章" || sections[0].Path != "OEBPS/b.xhtml" {
		t.Fatalf("structure %+v %v", sections, err)
	}
	job, err := s.Job(context.Background(), a.JobID)
	if err != nil || job.ParsedSections != 2 || job.UnderstoodSections != 2 {
		t.Fatalf("coverage: %+v %v", job, err)
	}
	navigation, err := s.store.Navigation(context.Background(), revision, 0, 100)
	if err != nil || len(navigation) != 3 || navigation[1].ParentID != navigation[0].ID || navigation[1].SectionID != sections[0].ID || navigation[1].Start == 0 {
		t.Fatalf("navigation: %+v %v", navigation, err)
	}
	text, err := s.Read(context.Background(), item.Location{RevisionID: revision, SectionID: sections[1].ID, End: sections[1].Runes}, 4096)
	if err != nil || !strings.Contains(text, "鞘翅目") || strings.Contains(text, "should not run") {
		t.Fatalf("text %q %v", text, err)
	}
	position, quote, err := s.FindExcerpt(context.Background(), item.Location{RevisionID: revision, SectionID: sections[1].ID}, "第一章保存稀有词：<em>鞘翅目</em>。", 4096)
	if err != nil || quote != "第一章保存稀有词：鞘翅目。" {
		t.Fatalf("lexical source location: %+v %q %v", position, quote, err)
	}
	check, err := s.Read(context.Background(), position, 4096)
	if err != nil || check != quote {
		t.Fatalf("lexical citation cannot be read back: %q %v", check, err)
	}
	root, err := s.Item(context.Background(), a.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	score, err := s.ExactMulti(context.Background(), root, root.Location, [][]float32{{1, 2}})
	if err != nil || score != 5 {
		t.Fatalf("bounded exact interaction: %g %v", score, err)
	}
}
func TestLargeOriginalAndUnsupportedFormat(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	raw := strings.Repeat("x", 17<<20)
	a, e := s.Accept(ctx, event("large", 1, ""), strings.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	r, rev, e := s.File(ctx, a.RevisionID, (16 << 20), 16)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if rev.Bytes != int64(len(raw)) || string(data) != strings.Repeat("x", 16) {
		t.Fatal("large file or range truncated")
	}
	unsupported := event("pdf", 1, "preserved PDF bytes")
	unsupported.MediaType = "application/pdf"
	b, e := s.Accept(ctx, unsupported, nil)
	if e != nil {
		t.Fatal(e)
	}
	var job catalog.Job
	s.store.DB.First(&job, "id=?", a.JobID)
	s.store.DB.Model(&job).Update("state", "done")
	if _, e = s.RunOnce(ctx); !IsUnsupported(e) {
		t.Fatalf("unsupported error %v", e)
	}
	j, e := s.Job(ctx, b.JobID)
	if e != nil || j.State != "failed" {
		t.Fatalf("job %+v %v", j, e)
	}
	r, _, e = s.File(ctx, b.RevisionID, 0, -1)
	if e != nil {
		t.Fatal(e)
	}
	data, _ = io.ReadAll(r)
	r.Close()
	if string(data) != "preserved PDF bytes" {
		t.Fatal("unsupported original lost")
	}
}

func TestFailedIndexReusesUnderstandingAndEncoding(t *testing.T) {
	s, models := fixture(t)
	ctx := context.Background()
	a, e := s.Accept(ctx, event("retry-cache", 1, "cached source understanding"), nil)
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.store.Claim(ctx, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	original := s.index
	s.index = &failIndex{Index: original}
	if e = s.process(ctx, j); e == nil {
		t.Fatal("expected index failure")
	}
	calls := models.understandCalls
	s.index = original
	if e = s.process(ctx, j); e != nil {
		t.Fatal(e)
	}
	if models.understandCalls != calls {
		t.Fatal("recomputed unchanged understanding on retry")
	}
	job, e := s.Job(ctx, a.JobID)
	if e != nil || job.State != "done" {
		t.Fatal("retry did not publish")
	}
}

type failIndex struct{ idx.Index }

func (f *failIndex) Upsert(context.Context, item.Retrieval, []idx.Record) error {
	return errors.New("injected index outage")
}

// Full source acceptance must feed both new search projections, then respect
// immediate authoritative deletion even while external cleanup is pending.
func TestPhysicalFullTextGraphAndSourceRecovery(t *testing.T) {
	address, uri := os.Getenv("BTW_TEST_ELASTICSEARCH_URL"), os.Getenv("BTW_TEST_NEO4J_URI")
	if address == "" || uri == "" {
		t.Skip("requires isolated Elasticsearch and Neo4j")
	}
	s, _ := fixture(t)
	ctx := context.Background()
	name := "btwtest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	lexical := &elastic.Client{URL: address, Index: name, Username: os.Getenv("BTW_TEST_ELASTICSEARCH_USER"), Password: os.Getenv("BTW_TEST_ELASTICSEARCH_PASSWORD")}
	t.Cleanup(func() {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, address+"/"+name, nil)
		request.SetBasicAuth(lexical.Username, lexical.Password)
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
		}
	})
	driver, err := neo4j.NewDriver(uri, neo4j.BasicAuth(os.Getenv("BTW_TEST_NEO4J_USER"), os.Getenv("BTW_TEST_NEO4J_PASSWORD"), ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { driver.Close(context.Background()) })
	contentGraph := &graph.Client{Driver: driver, Database: os.Getenv("BTW_TEST_NEO4J_DATABASE")}
	s.lexical, s.graph = lexical, contentGraph
	if err = s.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	title := "btwtest-concept-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	input := event("indexed", 1, "鞘翅目硬化的前翅保护后翅。")
	input.Title = title
	admission, err := s.Accept(ctx, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = contentGraph.DeleteRoot(context.Background(), admission.ItemID) })
	if _, err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	lexicalHits, err := lexical.Search(ctx, elastic.Query{Text: "鞘翅目", Phrases: []string{"保护后翅"}, Kinds: []string{"article"}, Limit: 10})
	if err != nil || len(lexicalHits) == 0 {
		t.Fatalf("full text did not index source: %+v %v", lexicalHits, err)
	}
	graphHits, err := contentGraph.Search(ctx, []string{title}, []string{"article"}, 1, 10)
	if err != nil || len(graphHits) == 0 {
		t.Fatalf("grounded concept not indexed: %+v %v", graphHits, err)
	}
	if graphHits[0].RevisionID != admission.RevisionID {
		t.Fatal("graph revision not tied to saved original")
	}
	deletion := event("indexed", 2, "")
	deletion.Title = title
	deletion.Operation = "delete"
	if _, err = s.Accept(ctx, deletion, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Item(ctx, admission.ItemID); !errors.Is(err, item.ErrNotFound) {
		t.Fatalf("deleted item visible before backend cleanup: %v", err)
	}
	if _, err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	lexicalHits, err = lexical.Search(ctx, elastic.Query{Text: "鞘翅目", Kinds: []string{"article"}, Limit: 10})
	if err != nil || len(lexicalHits) != 0 {
		t.Fatalf("full text deletion: %+v %v", lexicalHits, err)
	}
	graphHits, err = contentGraph.Search(ctx, []string{title}, []string{"article"}, 1, 10)
	if err != nil || len(graphHits) != 0 {
		t.Fatalf("graph deletion: %+v %v", graphHits, err)
	}
}
