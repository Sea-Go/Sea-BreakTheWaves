package communitysource

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
)

type immutableHTTPStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *immutableHTTPStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		body, ok := s.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20+1))
		if err != nil || len(body) > 64<<20 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if old, ok := s.objects[r.URL.Path]; ok && string(old) != string(body) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		s.objects[r.URL.Path] = append([]byte(nil), body...)
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestCoveragePublisherFreezesSeparatePrefixesAndAllSubjectSlices(t *testing.T) {
	db := pgPool(t)
	if err := InitializeCoverage(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `TRUNCATE warehouse_community.coverage_subject,
		warehouse_community.coverage_prefix`); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(ioDiscard{}, nil))
	comment, commentReceipts, commentFacts := commentBatch(t)
	like, likeReceipts, likeFacts := likeBatch(t)
	commentSource := &fakeSource{batch: comment, receipts: commentReceipts}
	likeSource := &fakeSource{batch: like, receipts: likeReceipts}
	commentAuthority, likeAuthority := fakeAuthority{facts: commentFacts}, fakeAuthority{facts: likeFacts}
	for _, fixture := range []struct {
		stream    Stream
		batch     eventing.Batch
		source    *fakeSource
		authority fakeAuthority
	}{{CommentStream(), comment, commentSource, commentAuthority}, {LikeStream(), like, likeSource, likeAuthority}} {
		consumer := &Consumer{DB: db, Source: fixture.source, Authority: fixture.authority, Stream: fixture.stream, Logger: logger}
		if result, err := consumer.RunOnce(context.Background()); err != nil || result.AcknowledgedOffset != fixture.batch.ToOffset {
			t.Fatalf("seed %s prefix: %+v %v", fixture.stream.Producer, result, err)
		}
	}
	objects := &immutableHTTPStore{objects: map[string][]byte{}}
	server := httptest.NewServer(objects)
	defer server.Close()
	commentPublisher := &CoveragePublisher{DB: db, Source: commentSource, Authority: commentAuthority,
		Stream: CommentStream(), S3Prefix: server.URL, Client: server.Client(), Logger: logger}
	likePublisher := &CoveragePublisher{DB: db, Source: likeSource, Authority: likeAuthority,
		Stream: LikeStream(), S3Prefix: server.URL, Client: server.Client(), Logger: logger}
	commentPublication, err := commentPublisher.Publish(context.Background(), 4, "comment_v1")
	if err != nil || len(commentPublication.Subjects) != 2 || commentPublication.Prefix.Manifest.Producer != CommentProducer {
		t.Fatalf("comment coverage: %+v %v", commentPublication, err)
	}
	likePublication, err := likePublisher.Publish(context.Background(), 2, "like_v1")
	if err != nil || len(likePublication.Subjects) != 1 || likePublication.Prefix.Manifest.Producer != LikeProducer {
		t.Fatalf("like coverage: %+v %v", likePublication, err)
	}
	if commentPublication.Prefix.ManifestSHA256 == likePublication.Prefix.ManifestSHA256 ||
		commentPublication.Prefix.Manifest.EventIndexURL == likePublication.Prefix.Manifest.EventIndexURL {
		t.Fatal("two producer sequences shared one prefix identity")
	}
	replay, err := commentPublisher.Publish(context.Background(), 4, "comment_v1")
	if err != nil || replay.Prefix.ManifestSHA256 != commentPublication.Prefix.ManifestSHA256 {
		t.Fatalf("coverage replay: %+v %v", replay, err)
	}
	if _, err := commentPublisher.Publish(context.Background(), 3, "comment_partial"); err == nil {
		t.Fatal("prefix ending inside an unrecorded batch boundary was frozen")
	}
	var prefixes, subjects, forbidden int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.coverage_prefix`).Scan(&prefixes); err != nil || prefixes != 2 {
		t.Fatalf("prefix publications=%d err=%v", prefixes, err)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.coverage_subject`).Scan(&subjects); err != nil || subjects != 3 {
		t.Fatalf("subject publications=%d err=%v", subjects, err)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='warehouse_community' AND column_name IN ('tenant_id','realm')`).Scan(&forbidden); err != nil || forbidden != 0 {
		t.Fatalf("coverage schema exposed forbidden identity columns: %d %v", forbidden, err)
	}
}
