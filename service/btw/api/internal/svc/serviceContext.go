package svc

import (
	"context"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/agentx"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/graph"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	recommend "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/engine"
	search "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/engine"
	"github.com/google/uuid"
	mc "github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type Search interface {
	Candidates(context.Context, item.Request) (item.Result, error)
	Search(context.Context, item.Request) (item.Result, error)
	Generate(context.Context, item.Request, bool, func(item.StreamEvent) error) (item.Result, error)
	Scenario(item.Request) (item.Scenario, error)
	Close() error
}
type Recommender interface {
	Recommend(context.Context, item.Request, func(item.StreamEvent) error) (item.Result, error)
}
type ServiceContext struct {
	Observed  *telemetry.Bundle
	Config    config.Config
	Content   *content.Service
	Search    Search
	Recommend Recommender
	close     func() error
	mu        sync.Mutex
	closing   bool
	requests  map[uint64]context.CancelFunc
	serial    uint64
	pending   sync.WaitGroup
}

func NewServiceContext(c config.Config) *ServiceContext {
	s, e := Open(context.Background(), c)
	if e != nil {
		panic(e)
	}
	return s
}
func Open(ctx context.Context, c config.Config) (out *ServiceContext, err error) {
	return OpenWithModel(ctx, c, nil)
}

// OpenWithModel accepts an injected generation model for isolated hosts. BTW
// still reads the active representation contract from DataCenter.
func OpenWithModel(ctx context.Context, c config.Config, m model.Model) (out *ServiceContext, err error) {
	return OpenWithDataCenter(ctx, c, m, nil)
}

func OpenWithDataCenter(ctx context.Context, c config.Config, m model.Model, identity datacenter.TokenSource) (out *ServiceContext, err error) {
	connector, err := datacenter.New(c.DataCenterURL, identity)
	if err != nil {
		return nil, err
	}
	manifest, err := connector.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	inferenceClient := connector.Inference()
	retrieval := manifest.Retrieval
	planningModel := m
	if m == nil {
		m, err = connector.Chat(manifest.Chat.Agent, "agent")
		if err != nil {
			return nil, err
		}
		planningModel, err = connector.Chat(manifest.Chat.Planning, "planning")
		if err != nil {
			return nil, err
		}
	}
	db, e := database.Open(ctx, database.Config{DSN: c.Storage.DSN, Schema: c.Storage.Schema, MaxOpenConns: 16, MaxIdleConns: 4, ConnMaxLifetimeSeconds: 300})
	if e != nil {
		return nil, e
	}
	sqlDB, e := db.DB()
	if e != nil {
		return nil, e
	}
	var milvus *mc.Client
	var graphDriver neo4j.Driver
	var engine Search
	var observed *telemetry.Bundle
	defer func() {
		if err != nil {
			if engine != nil {
				_ = engine.Close()
			}
			if milvus != nil {
				milvus.Close(ctx)
			}
			if graphDriver != nil {
				graphDriver.Close(ctx)
			}
			sqlDB.Close()
			if observed != nil {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = observed.Close(closeCtx)
			}
		}
	}()
	var objects blob.Store
	switch c.Storage.ObjectMode {
	case "local":
		objects, e = blob.NewLocal(c.Storage.Directory)
	case "s3":
		client, x := minio.New(c.Storage.S3Endpoint, &minio.Options{Creds: credentials.NewStaticV4(c.Storage.S3AccessKey, c.Storage.S3SecretKey, ""), Secure: c.Storage.S3Secure})
		e = x
		if e == nil {
			objects = &blob.S3{Client: client, Bucket: c.Storage.S3Bucket, Prefix: c.Storage.S3Prefix}
		}
	default:
		e = fmt.Errorf("unknown object mode")
	}
	if e != nil {
		return nil, e
	}
	milvus, e = mc.New(ctx, &mc.ClientConfig{Address: c.Milvus.Address, APIKey: c.Milvus.Token})
	if e != nil {
		return nil, e
	}
	index := &idx.Milvus{Client: milvus, BatchSize: c.Milvus.BatchSize, QueryTokenLimit: c.Milvus.QueryTokenLimit}
	lexical := &elastic.Client{URL: c.Elasticsearch.URL, Index: c.Elasticsearch.Index, Username: c.Elasticsearch.Username, Password: c.Elasticsearch.Password, APIKey: c.Elasticsearch.APIKey}
	graphDriver, e = neo4j.NewDriver(c.Neo4j.URI, neo4j.BasicAuth(c.Neo4j.Username, c.Neo4j.Password, ""))
	if e != nil {
		return nil, e
	}
	contentGraph := &graph.Client{Driver: graphDriver, Database: c.Neo4j.Database}
	if e = lexical.Verify(ctx); e != nil {
		return nil, e
	}
	if e = graphDriver.VerifyConnectivity(ctx); e != nil {
		return nil, e
	}
	catalog, e := content.New(db, objects, inferenceClient, index, nil, content.Config{Retrieval: retrieval, Lexical: lexical, Graph: contentGraph, MaxLexicalSectionBytes: c.Limits.LexicalSectionBytes, ItemPartitions: c.Storage.ItemPartitions, MaxUploadBytes: c.Limits.UploadBytes, MaxUncompressedBytes: c.Limits.UncompressedBytes, WindowRunes: c.Limits.WindowRunes, LeaseSeconds: c.Limits.LeaseSeconds, MaxAttempts: c.Limits.MaxAttempts})
	if e != nil {
		return nil, e
	}
	if e = catalog.Migrate(ctx); e != nil {
		return nil, e
	}
	if c.Observability.Endpoint != "" {
		observed, e = telemetry.New(ctx, telemetry.Config{Service: "btw-api", Environment: c.Observability.Environment, InstanceID: uuid.NewString(), Output: os.Stdout, Level: slog.LevelInfo, OTLPEndpoint: c.Observability.Endpoint, SampleRatio: 1})
		if e != nil {
			return nil, e
		}
		if e = observed.InstallGlobals(); e != nil {
			return nil, e
		}
	}
	engine, e = search.New(catalog, inferenceClient, index, inferenceClient, c.Scenarios, m, agentx.Config{}, retrieval, lexical, contentGraph, planningModel)
	if e != nil {
		return nil, e
	}
	out = &ServiceContext{Observed: observed, Config: c, Content: catalog, Search: engine, Recommend: recommend.New(engine, catalog)}
	out.close = func() error {
		e := engine.Close()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if observed != nil {
			e = errors.Join(e, observed.Close(closeCtx))
		}
		return errors.Join(e, milvus.Close(closeCtx), graphDriver.Close(closeCtx), sqlDB.Close())
	}
	return out, nil
}
func (s *ServiceContext) Close() error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	for _, cancel := range s.requests {
		cancel()
	}
	s.mu.Unlock()
	s.pending.Wait()
	if s.close != nil {
		return s.close()
	}
	return nil
}
func (s *ServiceContext) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/books" {
			limit := s.Config.Limits.QueryBytes
			if limit < 1 {
				limit = 1 << 20
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		ctx, cancel := context.WithCancel(r.Context())
		failure := &requestFailure{}
		ctx = context.WithValue(ctx, failureKey{}, failure)
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			cancel()
			http.Error(w, "service stopping", 503)
			return
		}
		if s.requests == nil {
			s.requests = map[uint64]context.CancelFunc{}
		}
		s.serial++
		id := s.serial
		s.requests[id] = cancel
		s.pending.Add(1)
		s.mu.Unlock()
		defer func() { cancel(); s.mu.Lock(); delete(s.requests, id); s.mu.Unlock(); s.pending.Done() }()
		if s.Observed != nil {
			observedCtx, stage, e := s.Observed.Begin(ctx, "serving", "serving.request")
			if e == nil {
				writer := &statusWriter{ResponseWriter: w}
				next(writer, r.WithContext(observedCtx))
				outcome := "succeeded"
				var cause error
				if writer.status >= 400 {
					outcome = "failed"
					cause = fmt.Errorf("HTTP %d", writer.status)
				}
				failure.mu.Lock()
				if failure.err != nil {
					outcome, cause = "failed", failure.err
				}
				failure.mu.Unlock()
				if ctx.Err() != nil {
					outcome = "cancelled"
					cause = ctx.Err()
				}
				stage.End(observedCtx, outcome, "", cause)
				return
			}
		}
		next(w, r.WithContext(ctx))
	}
}

func (s *ServiceContext) RecordCandidates(ctx context.Context, req item.Request, result *item.Result) {
	if s.Content == nil {
		return
	}
	events := make([]item.Behavior, 0, len(result.Hits))
	for i, h := range result.Hits {
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(req.RequestID+h.Item.ID+"candidate")).String()
		events = append(events, item.Behavior{EventID: id, RequestID: req.RequestID, UserID: req.Ontology.UserID, ItemID: h.Item.ID, Scenario: req.Scenario, RootID: h.Item.RootID, DuplicateID: h.Item.DuplicateID, SourceRevision: h.Item.RevisionID, Type: "candidate", Position: i + 1, OccurredAt: time.Now().UTC(), Features: h.ModelFeatures, LabelSource: "candidate"})
	}
	if len(events) > 0 {
		if _, err := s.Content.RecordEvents(ctx, events); err != nil {
			result.Warnings = append(result.Warnings, "candidate_recording_failed")
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(p)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Failure belongs to one request, including streams whose HTTP status is already 200.
type failureKey struct{}
type requestFailure struct {
	mu  sync.Mutex
	err error
}

func RecordFailure(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if f, ok := ctx.Value(failureKey{}).(*requestFailure); ok {
		f.mu.Lock()
		if f.err == nil {
			f.err = err
		}
		f.mu.Unlock()
	}
	if !errors.Is(err, context.Canceled) {
		slog.ErrorContext(ctx, "request failed", "error", err)
	}
}
