package favoritesource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
)

// LocalCoverageReader reads only the isolated localhost object fixture. It
// refuses redirects, credentials, remote hosts, and oversized objects.
type LocalCoverageReader struct{ Client *http.Client }

func (r LocalCoverageReader) Get(ctx context.Context, target string) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrContract
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if r.Client != nil {
		copy := *r.Client
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, ErrContract
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Join(ErrCoveragePending, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrCoveragePending
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil || len(body) > 32<<20 {
		return nil, ErrCoveragePending
	}
	return body, nil
}

func (p *SubjectRefV2Preflight) coverageURL(generation, kind, hash, ext string) string {
	return strings.TrimRight(p.S3Prefix, "/") + "/warehouse-coverage/" + generation + "/" + kind + "/" + hash + ext
}

func (p *SubjectRefV2Preflight) verifyCoverage(ctx context.Context, snapshot preflightSnapshot, report *SubjectRefV2Report) {
	u, err := url.Parse(p.S3Prefix)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		addFinding(report, "coverage_prefix_not_isolated", len(snapshot.publications))
		return
	}
	for _, publication := range snapshot.publications {
		if publication.producer != Producer || publication.through < 1 || publication.through > preflightMaxRows ||
			!coverageGeneration.MatchString(publication.generation) || !hashPattern.MatchString(publication.manifest) ||
			!hashPattern.MatchString(publication.indexHash) || !hashPattern.MatchString(publication.batchHash) {
			addFinding(report, "coverage_prefix_reference_mismatch", 1)
			continue
		}
		ref, index, code := p.verifyRoot(ctx, publication, snapshot.ods, snapshot.batches)
		if code != "" {
			addFinding(report, code, 1)
			continue
		}
		report.VerifiedCoverageRoots = append(report.VerifiedCoverageRoots, publication.manifest)
		for _, receipt := range snapshot.receipts {
			if receipt.manifest != publication.manifest {
				continue
			}
			if code := p.verifySubjectReceipt(ctx, publication, ref, index, receipt); code != "" {
				addFinding(report, code, 1)
			}
		}
	}
}

func (p *SubjectRefV2Preflight) object(ctx context.Context, target string) ([]byte, bool) {
	if !strings.HasPrefix(target, strings.TrimRight(p.S3Prefix, "/")+"/warehouse-coverage/") {
		return nil, false
	}
	body, err := p.Objects.Get(ctx, target)
	return body, err == nil
}

func canonicalEqual(body []byte, value any) bool {
	canonical, err := coverageCanonical(value)
	return err == nil && bytes.Equal(body, canonical)
}

func decodeJSONL[T any](body []byte) ([]T, bool) {
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return nil, false
	}
	lines := bytes.Split(body[:len(body)-1], []byte{'\n'})
	items := make([]T, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			return nil, false
		}
		var item T
		if json.Unmarshal(line, &item) != nil {
			return nil, false
		}
		items = append(items, item)
	}
	return items, true
}

func (p *SubjectRefV2Preflight) verifyRoot(ctx context.Context, publication preflightPublication, ods []preflightODS,
	pgBatches []preflightBatch) (sourcecoverage.GlobalPrefixRef, []sourcecoverage.EventIndexRow, string) {
	var zero sourcecoverage.GlobalPrefixRef
	manifestURL := p.coverageURL(publication.generation, "manifest", publication.manifest, ".json")
	body, ok := p.object(ctx, manifestURL)
	if !ok || coverageHash(body) != publication.manifest {
		return zero, nil, "coverage_manifest_bytes_mismatch"
	}
	var ref sourcecoverage.GlobalPrefixRef
	if json.Unmarshal(body, &ref) != nil || ref.ManifestSHA256 != "" || !canonicalEqual(body, ref) {
		return zero, nil, "coverage_manifest_bytes_mismatch"
	}
	ref.ManifestSHA256 = publication.manifest
	hash, err := sourcecoverage.GlobalManifestHash(ref)
	if err != nil || hash != publication.manifest || ref.BindingPolicyID != CoverageBindingPolicyID ||
		ref.Producer != Producer || ref.WarehouseConsumer != DefaultConsumer || ref.WarehouseGeneration != publication.generation ||
		ref.ThroughOffset != strconv.FormatInt(publication.through, 10) || ref.EventIndexSHA256 != publication.indexHash ||
		ref.BatchEvidenceSHA256 != publication.batchHash ||
		ref.EventIndexURL != p.coverageURL(publication.generation, "event-index", publication.indexHash, ".jsonl") ||
		ref.BatchEvidenceURL != p.coverageURL(publication.generation, "batch-evidence", publication.batchHash, ".jsonl") {
		return zero, nil, "coverage_manifest_reference_mismatch"
	}
	indexBody, ok := p.object(ctx, ref.EventIndexURL)
	if !ok || coverageHash(indexBody) != publication.indexHash {
		return zero, nil, "coverage_index_bytes_mismatch"
	}
	index, ok := decodeJSONL[sourcecoverage.EventIndexRow](indexBody)
	if !ok {
		return zero, nil, "coverage_index_bytes_mismatch"
	}
	check, checkHash, err := sourcecoverage.EventIndexJSONL(Producer, publication.through, index)
	if err != nil || checkHash != publication.indexHash || !bytes.Equal(check, indexBody) {
		return zero, nil, "coverage_index_bytes_mismatch"
	}
	if int64(len(ods)) < publication.through {
		return zero, nil, "coverage_index_ods_mismatch"
	}
	for i, row := range index {
		old := ods[i]
		var receipt eventing.Receipt
		receiptErr := json.Unmarshal(old.receipt, &receipt)
		canonicalSpec, specErr := coverageCanonical(json.RawMessage(old.eventSpec))
		canonicalIndexed, indexErr := coverageCanonical(json.RawMessage(row.EventSpec))
		if old.producer != Producer || old.offset != int64(i+1) || row.Offset != strconv.FormatInt(old.offset, 10) ||
			row.EventID != old.eventID || row.InputHash != old.sourceHash || row.Subject != old.subject ||
			receiptErr != nil || row.ReceiptID != receipt.ReceiptID || row.ReceivedAt != receipt.ReceivedAt ||
			specErr != nil || indexErr != nil || !bytes.Equal(canonicalSpec, canonicalIndexed) {
			return zero, nil, "coverage_index_ods_mismatch"
		}
	}
	batchBody, ok := p.object(ctx, ref.BatchEvidenceURL)
	if !ok || coverageHash(batchBody) != publication.batchHash {
		return zero, nil, "coverage_batch_bytes_mismatch"
	}
	batches, ok := decodeJSONL[sourcecoverage.BatchEvidence](batchBody)
	if !ok {
		return zero, nil, "coverage_batch_bytes_mismatch"
	}
	check, checkHash, err = sourcecoverage.BatchEvidenceJSONL(batches, publication.through)
	if err != nil || checkHash != publication.batchHash || !bytes.Equal(check, batchBody) ||
		sourcecoverage.VerifyBatchEvidence(Producer, index, batches) != nil {
		return zero, nil, "coverage_batch_bytes_mismatch"
	}
	stored := make([]preflightBatch, 0, len(batches))
	for _, b := range pgBatches {
		if b.consumer == DefaultConsumer && b.producer == Producer && b.from <= publication.through {
			stored = append(stored, b)
		}
	}
	if len(stored) != len(batches) {
		return zero, nil, "coverage_batch_pg_reference_mismatch"
	}
	for i, b := range batches {
		if b.FromOffset != strconv.FormatInt(stored[i].from, 10) || b.ToOffset != strconv.FormatInt(stored[i].to, 10) || b.BatchHash != stored[i].hash {
			return zero, nil, "coverage_batch_pg_reference_mismatch"
		}
	}
	return ref, index, ""
}

func (p *SubjectRefV2Preflight) verifySubjectReceipt(ctx context.Context, publication preflightPublication,
	prefix sourcecoverage.GlobalPrefixRef, full []sourcecoverage.EventIndexRow, stored preflightSubjectReceipt) string {
	if !hashPattern.MatchString(stored.receiptHash) || !hashPattern.MatchString(stored.sparseHash) {
		return "coverage_receipt_reference_mismatch"
	}
	receiptURL := p.coverageURL(publication.generation, "subject-receipt", stored.receiptHash, ".json")
	body, ok := p.object(ctx, receiptURL)
	if !ok || coverageHash(body) != stored.receiptHash {
		return "coverage_receipt_bytes_mismatch"
	}
	var ref sourcecoverage.SubjectCoverageRef
	if json.Unmarshal(body, &ref) != nil || ref.ReceiptSHA256 != "" || !canonicalEqual(body, ref) {
		return "coverage_receipt_bytes_mismatch"
	}
	ref.ReceiptSHA256 = stored.receiptHash
	hash, err := sourcecoverage.SubjectReceiptHash(ref)
	if err != nil || hash != stored.receiptHash || ref.Prefix != prefix || ref.Subject != stored.subject ||
		ref.SparseIndexSHA256 != stored.sparseHash || ref.EventCount != stored.eventCount {
		return "coverage_receipt_reference_mismatch"
	}
	sparse, sparseHash, count, err := sourcecoverage.SubjectIndexJSONL(stored.subject, full)
	if err != nil || sparseHash != stored.sparseHash || count != stored.eventCount {
		return "coverage_sparse_reference_mismatch"
	}
	subjectBody, err := coverageCanonical(stored.subject)
	if err != nil || ref.SparseIndexURL != p.coverageURL(publication.generation,
		"subject-index/"+coverageHash(subjectBody), sparseHash, ".jsonl") {
		return "coverage_sparse_reference_mismatch"
	}
	oldSparse, ok := p.object(ctx, ref.SparseIndexURL)
	if !ok || !bytes.Equal(oldSparse, sparse) {
		return "coverage_sparse_bytes_mismatch"
	}
	return ""
}
