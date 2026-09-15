package communitysource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind/communityauthority"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const CoverageSchemaVersion = 1

var (
	ErrCoveragePending  = errors.New("community source prefix is incomplete")
	ErrCoverageConflict = errors.New("community source coverage conflicts with immutable evidence")
	generationPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

type CoverageSubject struct {
	Issuer    string `json:"issuer"`
	SubjectID string `json:"subject_id"`
}

type CoverageEvent struct {
	Producer         string          `json:"producer"`
	Offset           string          `json:"offset"`
	EventID          string          `json:"event_id"`
	InputHash        string          `json:"input_hash"`
	ReceiptID        string          `json:"receipt_id"`
	ReceivedAt       string          `json:"received_at"`
	Subject          CoverageSubject `json:"subject_ref"`
	Predecessor      *string         `json:"predecessor_event_id"`
	EventSpec        json.RawMessage `json:"event_spec"`
	TechnicalReceipt json.RawMessage `json:"technical_receipt"`
}

type CoverageBatch struct {
	FromOffset string `json:"from_offset"`
	ToOffset   string `json:"to_offset"`
	BatchHash  string `json:"batch_hash"`
}

type PrefixManifest struct {
	SchemaVersion       int    `json:"schema_version"`
	Producer            string `json:"producer"`
	Origin              string `json:"origin"`
	ThroughOffset       string `json:"through_offset"`
	WarehouseConsumer   string `json:"warehouse_consumer"`
	WarehouseGeneration string `json:"warehouse_generation"`
	EventIndexURL       string `json:"event_index_url"`
	EventIndexSHA256    string `json:"event_index_sha256"`
	BatchEvidenceURL    string `json:"batch_evidence_url"`
	BatchEvidenceSHA256 string `json:"batch_evidence_sha256"`
}

type PrefixPublication struct {
	Manifest       PrefixManifest `json:"manifest"`
	ManifestURL    string         `json:"manifest_url"`
	ManifestSHA256 string         `json:"manifest_sha256"`
}

type SubjectCoverage struct {
	SchemaVersion       int             `json:"schema_version"`
	Producer            string          `json:"producer"`
	Subject             CoverageSubject `json:"subject_ref"`
	ThroughOffset       string          `json:"through_offset"`
	PrefixManifestURL   string          `json:"prefix_manifest_url"`
	PrefixManifestSHA   string          `json:"prefix_manifest_sha256"`
	SparseIndexURL      string          `json:"sparse_index_url"`
	SparseIndexSHA256   string          `json:"sparse_index_sha256"`
	EventCount          int64           `json:"event_count"`
	WarehouseGeneration string          `json:"warehouse_generation"`
}

type SubjectPublication struct {
	Coverage      SubjectCoverage `json:"coverage"`
	ReceiptURL    string          `json:"receipt_url"`
	ReceiptSHA256 string          `json:"receipt_sha256"`
}

type Publication struct {
	Prefix   PrefixPublication    `json:"prefix"`
	Subjects []SubjectPublication `json:"subjects"`
}

type CoveragePublisher struct {
	DB        *pgxpool.Pool
	Source    Source
	Authority Authority
	Stream    Stream
	S3Prefix  string
	Client    *http.Client
	Logger    *slog.Logger
}

type frozenCoverageRow struct {
	Offset      int64
	Event       eventing.Event
	Hash        string
	Receipt     eventing.Receipt
	Subject     CoverageSubject
	Predecessor *string
}

func (p *CoveragePublisher) Publish(ctx context.Context, through int64, generation string) (output Publication, resultErr error) {
	started := time.Now()
	if err := p.validate(); err != nil {
		return output, err
	}
	if through < 1 || through > 9007199254740991 || !generationPattern.MatchString(generation) {
		return output, ErrContract
	}
	p.Logger.InfoContext(ctx, "community coverage publication started", "event", "warehouse.community.coverage.started",
		"producer", p.Stream.Producer, "through_offset", through, "generation", generation)
	defer func() {
		outcome, code, level := "succeeded", "", slog.LevelInfo
		if resultErr != nil {
			outcome, code, level = "failed", "COVERAGE_PUBLISH_FAILED", slog.LevelError
			if errors.Is(resultErr, ErrContract) || errors.Is(resultErr, ErrCoverageConflict) {
				outcome, code, level = "rejected", "COVERAGE_CONTRACT_MISMATCH", slog.LevelWarn
			}
		}
		attrs := []slog.Attr{slog.String("event", "warehouse.community.coverage.finished"), slog.String("outcome", outcome),
			slog.Float64("duration_ms", float64(time.Since(started).Microseconds())/1000),
			slog.String("producer", p.Stream.Producer), slog.Int64("through_offset", through),
			slog.Int("subject_count", len(output.Subjects))}
		if resultErr != nil {
			attrs = append(attrs, slog.String("error_code", code), slog.String("error_type", fmt.Sprintf("%T", resultErr)),
				slog.String("error_message", resultErr.Error()))
		}
		p.Logger.LogAttrs(ctx, level, "community coverage publication finished", attrs...)
	}()
	rows, batches, err := p.readFrozen(ctx, through)
	if err != nil {
		return output, err
	}
	if err := p.verifyAuthority(ctx, rows); err != nil {
		return output, err
	}
	indexBody, indexHash, err := coverageEventJSONL(p.Stream.Producer, through, rows)
	if err != nil {
		return output, err
	}
	batchBody, batchHash, err := coverageBatchJSONL(rows, batches, through)
	if err != nil {
		return output, err
	}
	manifest := PrefixManifest{SchemaVersion: CoverageSchemaVersion, Producer: p.Stream.Producer, Origin: "1",
		ThroughOffset: strconv.FormatInt(through, 10), WarehouseConsumer: p.Stream.Consumer,
		WarehouseGeneration: generation, EventIndexSHA256: indexHash, BatchEvidenceSHA256: batchHash}
	manifest.EventIndexURL = p.objectURL(generation, "event-index", indexHash, ".jsonl")
	manifest.BatchEvidenceURL = p.objectURL(generation, "batch-evidence", batchHash, ".jsonl")
	manifestBody, err := canonicalJSON(manifest)
	if err != nil {
		return output, err
	}
	manifestHash := contentHash(manifestBody)
	manifestURL := p.objectURL(generation, "prefix-manifest", manifestHash, ".json")
	for _, object := range []struct {
		URL  string
		Body []byte
	}{{manifest.EventIndexURL, indexBody}, {manifest.BatchEvidenceURL, batchBody}, {manifestURL, manifestBody}} {
		if err := p.putFixed(ctx, object.URL, object.Body); err != nil {
			return output, err
		}
	}
	prefix := PrefixPublication{Manifest: manifest, ManifestURL: manifestURL, ManifestSHA256: manifestHash}
	subjects := uniqueSubjects(rows)
	publications := make([]SubjectPublication, 0, len(subjects))
	for _, subject := range subjects {
		sparseBody, count, err := coverageSubjectJSONL(subject, rows)
		if err != nil {
			return output, err
		}
		sparseHash := contentHash(sparseBody)
		sparseURL := p.objectURL(generation, "subject-index", sparseHash, ".jsonl")
		coverage := SubjectCoverage{SchemaVersion: CoverageSchemaVersion, Producer: p.Stream.Producer,
			Subject: subject, ThroughOffset: manifest.ThroughOffset, PrefixManifestURL: manifestURL,
			PrefixManifestSHA: manifestHash, SparseIndexURL: sparseURL, SparseIndexSHA256: sparseHash,
			EventCount: count, WarehouseGeneration: generation}
		receiptBody, err := canonicalJSON(coverage)
		if err != nil {
			return output, err
		}
		receiptHash := contentHash(receiptBody)
		receiptURL := p.objectURL(generation, "subject-receipt", receiptHash, ".json")
		if err := p.putFixed(ctx, sparseURL, sparseBody); err != nil {
			return output, err
		}
		if err := p.putFixed(ctx, receiptURL, receiptBody); err != nil {
			return output, err
		}
		publications = append(publications, SubjectPublication{Coverage: coverage,
			ReceiptURL: receiptURL, ReceiptSHA256: receiptHash})
	}
	output = Publication{Prefix: prefix, Subjects: publications}
	if err := p.record(ctx, output, through, generation); err != nil {
		return Publication{}, err
	}
	return output, nil
}

func (p *CoveragePublisher) validate() error {
	if p == nil || p.DB == nil || p.Source == nil || p.Authority == nil || p.Logger == nil ||
		(p.Stream != CommentStream() && p.Stream != LikeStream()) {
		return ErrContract
	}
	parsed, err := url.Parse(p.S3Prefix)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrContract
	}
	return nil
}

func (p *CoveragePublisher) httpClient() *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if p.Client != nil {
		copy := *p.Client
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

func (p *CoveragePublisher) objectURL(generation, kind, hash, suffix string) string {
	producer := strings.ReplaceAll(p.Stream.Producer, ".", "-")
	return strings.TrimRight(p.S3Prefix, "/") + "/warehouse-community/" + producer + "/" + generation + "/" + kind + "/" + hash + suffix
}

func (p *CoveragePublisher) get(ctx context.Context, target string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := p.httpClient().Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20+1))
	if err != nil || len(body) > 64<<20 {
		return nil, response.StatusCode, ErrCoverageConflict
	}
	return body, response.StatusCode, nil
}

func (p *CoveragePublisher) putFixed(ctx context.Context, target string, body []byte) error {
	old, status, err := p.get(ctx, target)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		if !bytes.Equal(old, body) {
			return ErrCoverageConflict
		}
		return nil
	}
	if status != http.StatusNotFound {
		return ErrCoveragePending
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	response, err := p.httpClient().Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrCoveragePending
	}
	old, status, err = p.get(ctx, target)
	if err != nil || status != http.StatusOK || !bytes.Equal(old, body) {
		return ErrCoverageConflict
	}
	return nil
}

func (p *CoveragePublisher) readFrozen(ctx context.Context, through int64) ([]frozenCoverageRow, []CoverageBatch, error) {
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	var committed int64
	if err := tx.QueryRow(ctx, `SELECT committed_offset FROM warehouse_community.consumer_cursor
		WHERE consumer=$1 AND producer=$2`, p.Stream.Consumer, p.Stream.Producer).Scan(&committed); err != nil || committed < through {
		return nil, nil, ErrCoveragePending
	}
	rows, err := tx.Query(ctx, `SELECT source_offset,event_spec,source_event_hash,technical_receipt,issuer,subject_id,predecessor_event_id
		FROM warehouse_community.ods_event WHERE producer=$1 AND source_offset BETWEEN 1 AND $2 ORDER BY source_offset`,
		p.Stream.Producer, through)
	if err != nil {
		return nil, nil, err
	}
	items := make([]frozenCoverageRow, 0, through)
	for rows.Next() {
		var item frozenCoverageRow
		var spec, receipt []byte
		if err := rows.Scan(&item.Offset, &spec, &item.Hash, &receipt, &item.Subject.Issuer,
			&item.Subject.SubjectID, &item.Predecessor); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if json.Unmarshal(spec, &item.Event) != nil || json.Unmarshal(receipt, &item.Receipt) != nil {
			rows.Close()
			return nil, nil, ErrCoverageConflict
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	if int64(len(items)) != through {
		return nil, nil, ErrCoveragePending
	}
	for index, item := range items {
		if item.Offset != int64(index+1) {
			return nil, nil, ErrCoveragePending
		}
	}
	batchRows, err := tx.Query(ctx, `SELECT from_offset,to_offset,batch_hash FROM warehouse_community.read_batch_evidence
		WHERE consumer=$1 AND producer=$2 AND from_offset<=$3 ORDER BY from_offset`, p.Stream.Consumer, p.Stream.Producer, through)
	if err != nil {
		return nil, nil, err
	}
	batches := []CoverageBatch{}
	for batchRows.Next() {
		var from, to int64
		var hash string
		if err := batchRows.Scan(&from, &to, &hash); err != nil {
			batchRows.Close()
			return nil, nil, err
		}
		if to > through {
			continue
		}
		batches = append(batches, CoverageBatch{FromOffset: strconv.FormatInt(from, 10),
			ToOffset: strconv.FormatInt(to, 10), BatchHash: hash})
	}
	if err := batchRows.Err(); err != nil {
		batchRows.Close()
		return nil, nil, err
	}
	batchRows.Close()
	if len(batches) == 0 || batches[len(batches)-1].ToOffset != strconv.FormatInt(through, 10) {
		return nil, nil, ErrCoveragePending
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return items, batches, nil
}

func (p *CoveragePublisher) verifyAuthority(ctx context.Context, rows []frozenCoverageRow) error {
	for _, row := range rows {
		receipt, err := p.Source.EventReceipt(ctx, p.Stream.Producer, row.Event.EventID)
		if err != nil || receipt != row.Receipt || receipt.Offset != row.Offset || receipt.InputHash != row.Hash {
			return ErrCoverageConflict
		}
		fact, err := p.Authority.Lookup(ctx, communityauthority.Evidence{Event: row.Event,
			InputHash: row.Hash, Offset: row.Offset, Receipt: receipt})
		if err != nil || fact.SubjectRef.Issuer != row.Subject.Issuer || fact.SubjectRef.SubjectID != row.Subject.SubjectID {
			return ErrCoverageConflict
		}
		if fact.PredecessorEventID == "" && row.Predecessor != nil || fact.PredecessorEventID != "" &&
			(row.Predecessor == nil || fact.PredecessorEventID != *row.Predecessor) {
			return ErrCoverageConflict
		}
	}
	return nil
}

func coverageEventJSONL(producer string, through int64, rows []frozenCoverageRow) ([]byte, string, error) {
	if int64(len(rows)) != through {
		return nil, "", ErrCoveragePending
	}
	var output bytes.Buffer
	for index, row := range rows {
		if row.Offset != int64(index+1) || row.Event.Producer != producer || row.Receipt.Offset != row.Offset ||
			row.Hash != row.Receipt.InputHash || !digest.MatchString(row.Hash) ||
			row.Subject.Issuer != "rtw.identity" || !positiveID(row.Subject.SubjectID) {
			return nil, "", ErrCoverageConflict
		}
		spec, err := json.Marshal(row.Event)
		if err != nil {
			return nil, "", err
		}
		receipt, err := json.Marshal(row.Receipt)
		if err != nil {
			return nil, "", err
		}
		value := CoverageEvent{Producer: producer, Offset: strconv.FormatInt(row.Offset, 10), EventID: row.Event.EventID,
			InputHash: row.Hash, ReceiptID: row.Receipt.ReceiptID, ReceivedAt: row.Receipt.ReceivedAt,
			Subject: row.Subject, Predecessor: row.Predecessor, EventSpec: spec, TechnicalReceipt: receipt}
		line, err := canonicalJSON(value)
		if err != nil {
			return nil, "", err
		}
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.Bytes(), contentHash(output.Bytes()), nil
}

func coverageBatchJSONL(rows []frozenCoverageRow, batches []CoverageBatch, through int64) ([]byte, string, error) {
	var output bytes.Buffer
	next := int64(1)
	for _, batch := range batches {
		from, errFrom := strconv.ParseInt(batch.FromOffset, 10, 64)
		to, errTo := strconv.ParseInt(batch.ToOffset, 10, 64)
		if errFrom != nil || errTo != nil || from != next || to < from || to > int64(len(rows)) || !digest.MatchString(batch.BatchHash) {
			return nil, "", ErrCoverageConflict
		}
		items := make([]eventing.Item, 0, to-from+1)
		for _, row := range rows[from-1 : to] {
			items = append(items, eventing.Item{Offset: row.Offset, InputHash: row.Hash, Event: row.Event})
		}
		canonical, err := canonicalJSON(items)
		if err != nil || contentHash(canonical) != batch.BatchHash {
			return nil, "", ErrCoverageConflict
		}
		line, err := canonicalJSON(batch)
		if err != nil {
			return nil, "", err
		}
		output.Write(line)
		output.WriteByte('\n')
		next = to + 1
	}
	if next != through+1 {
		return nil, "", ErrCoveragePending
	}
	return output.Bytes(), contentHash(output.Bytes()), nil
}

func uniqueSubjects(rows []frozenCoverageRow) []CoverageSubject {
	seen := map[CoverageSubject]struct{}{}
	for _, row := range rows {
		seen[row.Subject] = struct{}{}
	}
	result := make([]CoverageSubject, 0, len(seen))
	for subject := range seen {
		result = append(result, subject)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Issuer != result[j].Issuer {
			return result[i].Issuer < result[j].Issuer
		}
		return result[i].SubjectID < result[j].SubjectID
	})
	return result
}

func coverageSubjectJSONL(subject CoverageSubject, rows []frozenCoverageRow) ([]byte, int64, error) {
	var output bytes.Buffer
	var count int64
	for _, row := range rows {
		if row.Subject != subject {
			continue
		}
		spec, _ := json.Marshal(row.Event)
		receipt, _ := json.Marshal(row.Receipt)
		line, err := canonicalJSON(CoverageEvent{Producer: row.Event.Producer, Offset: strconv.FormatInt(row.Offset, 10),
			EventID: row.Event.EventID, InputHash: row.Hash, ReceiptID: row.Receipt.ReceiptID,
			ReceivedAt: row.Receipt.ReceivedAt, Subject: row.Subject, Predecessor: row.Predecessor,
			EventSpec: spec, TechnicalReceipt: receipt})
		if err != nil {
			return nil, 0, err
		}
		output.Write(line)
		output.WriteByte('\n')
		count++
	}
	return output.Bytes(), count, nil
}

func (p *CoveragePublisher) record(ctx context.Context, publication Publication, through int64, generation string) error {
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	prefix := publication.Prefix
	if _, err := tx.Exec(ctx, `INSERT INTO warehouse_community.coverage_prefix
		(producer,generation,through_offset,event_index_sha256,batch_evidence_sha256,manifest_sha256,manifest_url)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, p.Stream.Producer, generation, through,
		prefix.Manifest.EventIndexSHA256, prefix.Manifest.BatchEvidenceSHA256, prefix.ManifestSHA256, prefix.ManifestURL); err != nil {
		return err
	}
	var manifestHash, manifestURL string
	var gotThrough int64
	if err := tx.QueryRow(ctx, `SELECT through_offset,manifest_sha256,manifest_url FROM warehouse_community.coverage_prefix
		WHERE producer=$1 AND generation=$2`, p.Stream.Producer, generation).Scan(&gotThrough, &manifestHash, &manifestURL); err != nil ||
		gotThrough != through || manifestHash != prefix.ManifestSHA256 || manifestURL != prefix.ManifestURL {
		return ErrCoverageConflict
	}
	for _, subject := range publication.Subjects {
		if _, err := tx.Exec(ctx, `INSERT INTO warehouse_community.coverage_subject
			(producer,prefix_manifest_sha256,issuer,subject_id,event_count,sparse_index_sha256,receipt_sha256,receipt_url)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, p.Stream.Producer, prefix.ManifestSHA256,
			subject.Coverage.Subject.Issuer, subject.Coverage.Subject.SubjectID, subject.Coverage.EventCount,
			subject.Coverage.SparseIndexSHA256, subject.ReceiptSHA256, subject.ReceiptURL); err != nil {
			return err
		}
		var count int64
		var sparseHash, receiptHash, receiptURL string
		if err := tx.QueryRow(ctx, `SELECT event_count,sparse_index_sha256,receipt_sha256,receipt_url
			FROM warehouse_community.coverage_subject WHERE producer=$1 AND prefix_manifest_sha256=$2 AND issuer=$3 AND subject_id=$4`,
			p.Stream.Producer, prefix.ManifestSHA256, subject.Coverage.Subject.Issuer, subject.Coverage.Subject.SubjectID).
			Scan(&count, &sparseHash, &receiptHash, &receiptURL); err != nil || count != subject.Coverage.EventCount ||
			sparseHash != subject.Coverage.SparseIndexSHA256 || receiptHash != subject.ReceiptSHA256 || receiptURL != subject.ReceiptURL {
			return ErrCoverageConflict
		}
	}
	return tx.Commit(ctx)
}

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsoncanonicalizer.Transform(raw)
}

func contentHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
