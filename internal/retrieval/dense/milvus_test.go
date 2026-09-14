package dense

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/milvus-io/milvus-proto/go-api/v2/commonpb"
	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus-proto/go-api/v2/schemapb"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type wireServer struct {
	milvuspb.UnimplementedMilvusServiceServer
	schema   *entity.Schema
	snapshot *Snapshot
	t        *testing.T
	bad      atomic.Bool
	fail     atomic.Bool
	calls    atomic.Int64
}

func (w *wireServer) DescribeCollection(_ context.Context, q *milvuspb.DescribeCollectionRequest) (*milvuspb.DescribeCollectionResponse, error) {
	return &milvuspb.DescribeCollectionResponse{Status: &commonpb.Status{}, CollectionName: q.CollectionName, Schema: w.schema.ProtoMessage(), ConsistencyLevel: commonpb.ConsistencyLevel_Strong}, nil
}
func (w *wireServer) Search(_ context.Context, q *milvuspb.SearchRequest) (*milvuspb.SearchResults, error) {
	w.calls.Add(1)
	if w.fail.Load() {
		return nil, status.Error(codes.Internal, "fixture backend unavailable")
	}
	if q.ConsistencyLevel != commonpb.ConsistencyLevel_Strong || q.UseDefaultConsistency || q.Dsl != "index_hash == "+jsonLiteral(w.snapshot.ref.SHA256)+" and revision_id in [\"r1\"]" {
		w.t.Error("missing strong read / pre-ANN scope", q)
	}
	if !strings.Contains(q.Dsl, jsonLiteral(w.snapshot.ref.SHA256)) {
		w.t.Error("index hash not pinned")
	}
	params := entity.KvPairsMap(q.SearchParams)
	var ann map[string]int
	_ = json.Unmarshal([]byte(params["params"]), &ann)
	if ann["ef"] != 32 || params["anns_field"] != "vector" || params["metric_type"] != "COSINE" {
		w.t.Error("missing ef/field", params)
	}
	id := "b"
	revision := "r1"
	if w.bad.Load() {
		revision = "foreign"
	}
	return &milvuspb.SearchResults{Status: &commonpb.Status{}, Results: &schemapb.SearchResultData{NumQueries: 1, TopK: 1, Topks: []int64{1}, Ids: &schemapb.IDs{IdField: &schemapb.IDs_StrId{StrId: &schemapb.StringArray{Data: []string{id}}}}, Scores: []float32{.60000002384}, FieldsData: []*schemapb.FieldData{column.NewColumnVarChar("chunk_id", []string{id}).FieldData(), column.NewColumnVarChar("revision_id", []string{revision}).FieldData(), column.NewColumnVarChar("vector_hash", []string{w.snapshot.rows[id].VectorHash}).FieldData()}}}, nil
}
func TestMilvusSDKSearchWireAndFailure(t *testing.T) {
	s, objects, _, built, _ := fixtureService(t)
	snapshot, err := s.Open(context.Background(), built.Ref)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	wire := &wireServer{snapshot: snapshot, t: t}
	milvuspb.RegisterMilvusServiceServer(server, wire)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: listener.Addr().String(), DisableConn: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	service, err := NewMilvus(objects, fixtureEncoder(), fixtureConfig(), client, MilvusConfig{Namespace: "fixture", M: 16, EFConstruction: 128, EFSearch: 32})
	if err != nil {
		t.Fatal(err)
	}
	m := service.backend.(*milvus)
	wire.schema = m.schema(snapshot)
	q := queryFor(built)
	q.TopK = 1
	q.ValidRevisionIDs = []string{"r1"}
	result, err := service.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Chunk.ID != "b" || math.Abs(result.Candidates[0].Score-.6) > 1e-14 || result.Candidates[0].BackendScore == result.Candidates[0].Score {
		t.Fatal(result)
	}
	wire.bad.Store(true)
	if _, err = service.Search(ctx, q); err == nil {
		t.Fatal("accepted foreign revision from backend")
	}
	wire.bad.Store(false)
	wire.fail.Store(true)
	if _, err = service.Search(ctx, q); err == nil {
		t.Fatal("backend error silently fell back to exact")
	}
	if wire.calls.Load() != 3 {
		t.Fatal(wire.calls.Load())
	}
}
func TestMilvusProjectionIdentityAndFloat32Bounds(t *testing.T) {
	s, _, _, built, _ := fixtureService(t)
	snapshot, err := s.Open(context.Background(), built.Ref)
	if err != nil {
		t.Fatal(err)
	}
	m := &milvus{config: MilvusConfig{Namespace: "fixture", M: 16, EFConstruction: 128, EFSearch: 32}}
	name, description := m.identity(snapshot)
	if !strings.HasPrefix(name, "sea_dense_fixture_") || !strings.Contains(description, built.Ref.SHA256) {
		t.Fatal(name, description)
	}
	other := *m
	other.config.M = 32
	if n, _ := other.identity(snapshot); n == name {
		t.Fatal("index params not bound")
	}
	if _, err = toFloat32([]float64{math.MaxFloat64, 1}); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, err = toFloat32([]float64{math.SmallestNonzeroFloat64, 0}); err == nil {
		t.Fatal("underflow accepted")
	}
	for _, bad := range []MilvusConfig{{Namespace: "", M: 16, EFConstruction: 128, EFSearch: 32}, {Namespace: "../existing", M: 16, EFConstruction: 128, EFSearch: 32}, {Namespace: "fixture", M: 0, EFConstruction: 128, EFSearch: 32}} {
		if _, err = NewMilvus(s.objects, s.encoder, s.config, &milvusclient.Client{}, bad); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
}
