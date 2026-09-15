package communitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind/communityauthority"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mutatingSource struct {
	Source
	mutate  func(eventing.Batch) eventing.Batch
	failACK bool
}

func (s *mutatingSource) ReadEvents(ctx context.Context, consumer, producer string, limit int) (eventing.Batch, error) {
	batch, err := s.Source.ReadEvents(ctx, consumer, producer, limit)
	if err != nil || s.mutate == nil || len(batch.Events) == 0 {
		return batch, err
	}
	return s.mutate(batch), nil
}

func (s *mutatingSource) AcknowledgeEvents(ctx context.Context, consumer string, ack eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	if s.failACK {
		s.failACK = false
		return eventing.DeliveryReceipt{}, errors.New("acceptance lost ACK response")
	}
	return s.Source.AcknowledgeEvents(ctx, consumer, ack)
}

type predecessorAuthority struct {
	Authority
	eventID string
}

func (a predecessorAuthority) Lookup(ctx context.Context, evidence communityauthority.Evidence) (communityauthority.Fact, error) {
	fact, err := a.Authority.Lookup(ctx, evidence)
	if err == nil && evidence.Event.EventID == a.eventID {
		fact.PredecessorEventID = "rtw.comment.interaction.missing"
	}
	return fact, err
}

func acceptanceLogger() *slog.Logger {
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.TimeKey {
			return slog.String("timestamp", attr.Value.Time().UTC().Format(time.RFC3339Nano))
		}
		if attr.Key == slog.LevelKey {
			return slog.String("level", strings.ToLower(attr.Value.String()))
		}
		if attr.Key == slog.MessageKey {
			return slog.String("message", attr.Value.String())
		}
		return attr
	}})
	return slog.New(handler).With("service", "sea-btw-community-warehouse-acceptance", "environment", "test",
		"service_version", os.Getenv("COMMUNITY_WAREHOUSE_BTW_SHA"), "instance_id", "community-warehouse-test",
		"component", "warehouse", "log_source", "application")
}

func TestRealCommunityWarehouseChain(t *testing.T) {
	required := []string{"COMMUNITY_WAREHOUSE_REAL_DSN", "COMMUNITY_WAREHOUSE_DC_URL", "COMMUNITY_WAREHOUSE_DC_TOKEN",
		"COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_URL", "COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_TOKEN",
		"COMMUNITY_WAREHOUSE_LIKE_AUTHORITY_URL", "COMMUNITY_WAREHOUSE_LIKE_AUTHORITY_TOKEN",
		"COMMUNITY_WAREHOUSE_COMMENT_EVENT_IDS", "COMMUNITY_WAREHOUSE_LIKE_EVENT_IDS",
		"COMMUNITY_WAREHOUSE_S3_PREFIX", "COMMUNITY_WAREHOUSE_ODS_OUTPUT", "COMMUNITY_WAREHOUSE_PUBLICATION_OUTPUT",
		"COMMUNITY_WAREHOUSE_BTW_SHA"}
	for _, key := range required {
		if os.Getenv(key) == "" {
			t.Skip("run through warehouse/community/acceptance.sh")
		}
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, os.Getenv("COMMUNITY_WAREHOUSE_REAL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Initialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := InitializeCoverage(ctx, db); err != nil {
		t.Fatal(err)
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("COMMUNITY_WAREHOUSE_DC_URL"),
		Token: os.Getenv("COMMUNITY_WAREHOUSE_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	commentAuthority, err := communityauthority.New(communityauthority.Config{BaseURL: os.Getenv("COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_URL"),
		Token: os.Getenv("COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_TOKEN"), Producer: CommentProducer})
	if err != nil {
		t.Fatal(err)
	}
	likeAuthority, err := communityauthority.New(communityauthority.Config{BaseURL: os.Getenv("COMMUNITY_WAREHOUSE_LIKE_AUTHORITY_URL"),
		Token: os.Getenv("COMMUNITY_WAREHOUSE_LIKE_AUTHORITY_TOKEN"), Producer: LikeProducer})
	if err != nil {
		t.Fatal(err)
	}
	commentIDs := exactIDs(t, os.Getenv("COMMUNITY_WAREHOUSE_COMMENT_EVENT_IDS"), 4)
	likeIDs := exactIDs(t, os.Getenv("COMMUNITY_WAREHOUSE_LIKE_EVENT_IDS"), 2)
	assertRealBatch(t, ctx, dc, CommentStream(), commentIDs)
	assertRealBatch(t, ctx, dc, LikeStream(), likeIDs)
	logger := acceptanceLogger()
	negative := map[string]bool{}

	wrongAuthorityServer := authorityMutationProxy(t, os.Getenv("COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_URL"),
		os.Getenv("COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_TOKEN"))
	wrongAuthority, err := communityauthority.New(communityauthority.Config{BaseURL: wrongAuthorityServer.URL,
		Token: os.Getenv("COMMUNITY_WAREHOUSE_COMMENT_AUTHORITY_TOKEN"), Producer: CommentProducer, Client: wrongAuthorityServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	negative["wrong_authority"] = rejectedWithoutODS(t, db, &Consumer{DB: db, Source: dc, Authority: wrongAuthority,
		Stream: CommentStream(), Logger: logger})
	wrongAuthorityServer.Close()

	wrongHash := &mutatingSource{Source: dc, mutate: func(batch eventing.Batch) eventing.Batch {
		batch.Events[0].InputHash = testContentHash([]byte("wrong-source-hash"))
		canonical, _ := testCanonicalJSON(batch.Events)
		batch.BatchHash = testContentHash(canonical)
		return batch
	}}
	negative["wrong_hash"] = rejectedWithoutODS(t, db, &Consumer{DB: db, Source: wrongHash, Authority: commentAuthority,
		Stream: CommentStream(), Logger: logger})

	missingOffset := &mutatingSource{Source: dc, mutate: func(batch eventing.Batch) eventing.Batch {
		batch.Events = batch.Events[1:]
		batch.FromOffset = batch.Events[0].Offset
		canonical, _ := testCanonicalJSON(batch.Events)
		batch.BatchHash = testContentHash(canonical)
		return batch
	}}
	negative["missing_offset"] = rejectedWithoutODS(t, db, &Consumer{DB: db, Source: missingOffset, Authority: commentAuthority,
		Stream: CommentStream(), Logger: logger})

	negative["wrong_predecessor"] = rejectedWithoutODS(t, db, &Consumer{DB: db, Source: dc,
		Authority: predecessorAuthority{Authority: commentAuthority, eventID: commentIDs[2]},
		Stream:    CommentStream(), Logger: logger})

	for _, fixture := range []struct {
		stream    Stream
		authority Authority
		through   int64
	}{{CommentStream(), commentAuthority, 4}, {LikeStream(), likeAuthority, 2}} {
		lost := &mutatingSource{Source: dc, failACK: true}
		consumer := &Consumer{DB: db, Source: lost, Authority: fixture.authority, Stream: fixture.stream, Logger: logger}
		first, err := consumer.RunOnce(ctx)
		if err == nil || first.CommittedOffset != fixture.through || first.AcknowledgedOffset != 0 || first.NewRows != int(fixture.through) {
			t.Fatalf("%s lost ACK boundary: %+v %v", fixture.stream.Producer, first, err)
		}
		restarted := &Consumer{DB: db, Source: dc, Authority: fixture.authority, Stream: fixture.stream, Logger: logger}
		second, err := restarted.RunOnce(ctx)
		if err != nil || second.ReplayedRows != int(fixture.through) || second.NewRows != 0 || second.AcknowledgedOffset != fixture.through {
			t.Fatalf("%s restart replay: %+v %v", fixture.stream.Producer, second, err)
		}
		negative[fixture.stream.Producer+"_lost_ack_replay"] = true
	}
	assertRealODS(t, ctx, db, commentIDs, likeIDs)

	commentPublisher := &CoveragePublisher{DB: db, Source: dc, Authority: commentAuthority, Stream: CommentStream(),
		S3Prefix: os.Getenv("COMMUNITY_WAREHOUSE_S3_PREFIX"), Logger: logger}
	likePublisher := &CoveragePublisher{DB: db, Source: dc, Authority: likeAuthority, Stream: LikeStream(),
		S3Prefix: os.Getenv("COMMUNITY_WAREHOUSE_S3_PREFIX"), Logger: logger}
	commentPublication, err := commentPublisher.Publish(ctx, 4, "comment_v1")
	if err != nil || len(commentPublication.Subjects) != 2 {
		t.Fatalf("real comment coverage: %+v %v", commentPublication, err)
	}
	likePublication, err := likePublisher.Publish(ctx, 2, "like_v1")
	if err != nil || len(likePublication.Subjects) != 1 {
		t.Fatalf("real like coverage: %+v %v", likePublication, err)
	}
	if commentPublication.Prefix.ManifestSHA256 == likePublication.Prefix.ManifestSHA256 ||
		commentPublication.Prefix.Manifest.Producer == likePublication.Prefix.Manifest.Producer {
		t.Fatal("real producer prefixes were merged")
	}
	if replay, err := commentPublisher.Publish(ctx, 4, "comment_v1"); err != nil ||
		replay.Prefix.ManifestSHA256 != commentPublication.Prefix.ManifestSHA256 {
		t.Fatalf("real coverage replay: %+v %v", replay, err)
	}
	ods, err := ExportODS(ctx, db)
	if err != nil || os.WriteFile(os.Getenv("COMMUNITY_WAREHOUSE_ODS_OUTPUT"), ods, 0600) != nil {
		t.Fatalf("write real ODS export: bytes=%d err=%v", len(ods), err)
	}
	publication := map[string]any{"comment": commentPublication, "like": likePublication, "negative_checks": negative}
	body, err := json.MarshalIndent(publication, "", "  ")
	if err != nil || os.WriteFile(os.Getenv("COMMUNITY_WAREHOUSE_PUBLICATION_OUTPUT"), append(body, '\n'), 0600) != nil {
		t.Fatalf("write real coverage publication: %v", err)
	}
}

func exactIDs(t *testing.T, raw string, expected int) []string {
	t.Helper()
	ids := strings.Split(raw, ",")
	if len(ids) != expected {
		t.Fatalf("event IDs=%d want=%d", len(ids), expected)
	}
	return ids
}

func assertRealBatch(t *testing.T, ctx context.Context, dc *datacenter.Client, stream Stream, ids []string) {
	t.Helper()
	batch, err := dc.ReadEvents(ctx, stream.Consumer, stream.Producer, 128)
	if err != nil || len(batch.Events) != len(ids) || batch.FromOffset != 1 || batch.ToOffset != int64(len(ids)) {
		t.Fatalf("real %s batch: %+v %v", stream.Producer, batch, err)
	}
	for index, item := range batch.Events {
		if item.Offset != int64(index+1) || item.Event.EventID != ids[index] {
			t.Fatalf("real %s offset %d: %+v", stream.Producer, index+1, item)
		}
	}
}

func rejectedWithoutODS(t *testing.T, db *pgxpool.Pool, consumer *Consumer) bool {
	t.Helper()
	if _, err := consumer.RunOnce(context.Background()); err == nil {
		t.Fatal("invalid real source was admitted")
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM warehouse_community.ods_event`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected real source changed ODS: %d %v", count, err)
	}
	return true
}

func authorityMutationProxy(t *testing.T, upstream, token string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.RequestURI(), nil)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
		if err != nil || response.StatusCode != http.StatusOK {
			w.WriteHeader(response.StatusCode)
			return
		}
		var value map[string]any
		if json.Unmarshal(body, &value) != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		value["subject_ref"].(map[string]any)["subject_id"] = "9999"
		_ = json.NewEncoder(w).Encode(value)
	}))
}

func assertRealODS(t *testing.T, ctx context.Context, db *pgxpool.Pool, commentIDs, likeIDs []string) {
	t.Helper()
	rows, err := db.Query(ctx, `SELECT producer,source_offset,event_id,issuer,subject_id,target_revision,revision_status,
		search_evidence,technical_receipt->>'event_id',technical_receipt->>'input_hash',predecessor_event_id
		FROM warehouse_community.ods_event ORDER BY producer,source_offset`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string][]string{}
	for rows.Next() {
		var producer, eventID, issuer, subjectID, revisionStatus, receiptEventID, receiptHash string
		var offset int64
		var targetRevision, predecessor *string
		var searchEvidence *bool
		if err := rows.Scan(&producer, &offset, &eventID, &issuer, &subjectID, &targetRevision, &revisionStatus,
			&searchEvidence, &receiptEventID, &receiptHash, &predecessor); err != nil {
			t.Fatal(err)
		}
		if issuer != "rtw.identity" || targetRevision != nil || revisionStatus != "unknown" ||
			receiptEventID != eventID || !digest.MatchString(receiptHash) || subjectID == "" {
			t.Fatalf("real ODS source precision: producer=%s offset=%d event=%s", producer, offset, eventID)
		}
		if producer == CommentProducer && (searchEvidence == nil || *searchEvidence) ||
			producer == LikeProducer && searchEvidence != nil {
			t.Fatalf("real ODS search evidence: producer=%s offset=%d value=%v", producer, offset, searchEvidence)
		}
		seen[producer] = append(seen[producer], eventID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(seen[CommentProducer], commentIDs) || !equalStrings(seen[LikeProducer], likeIDs) {
		t.Fatalf("real ODS producer sequences: %v", seen)
	}
	var forbidden int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='warehouse_community'
		AND column_name IN ('tenant_id','realm')`).Scan(&forbidden); err != nil || forbidden != 0 {
		t.Fatalf("real ODS exposed forbidden identity columns: %d %v", forbidden, err)
	}
}

func equalStrings(left, right []string) bool {
	return bytes.Equal([]byte(strings.Join(left, "\x00")), []byte(strings.Join(right, "\x00")))
}
