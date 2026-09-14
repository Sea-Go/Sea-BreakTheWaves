// Package searchsource owns the warehouse's independent DC cursor for RTW
// human search judgments. It does not assign relevance or claim qrel coverage.
package searchsource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const Producer = "ridethewind.knowledge"
const DefaultConsumer = "btw-warehouse-search-qrel"
const Revised = "knowledge.search.judgment.revised.v1"
const Withdrawn = "knowledge.search.judgment.withdrawn.v1"

var ErrContract = errors.New("warehouse search judgment source contract mismatch")
var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Judgment struct {
	JudgmentID          string `json:"judgment_id"`
	RevisionID          string `json:"judgment_revision_id"`
	JudgmentRevision    int    `json:"judgment_revision"`
	BaseRevisionID      string `json:"base_revision_id"`
	SearchID            string `json:"search_id"`
	QueryText           string `json:"query_text"`
	QueryTextSHA256     string `json:"query_text_sha256"`
	QueryTime           string `json:"query_time"`
	ModuleID            string `json:"module_id"`
	ReleaseID           string `json:"release_id"`
	Generation          int64  `json:"generation"`
	PublicationRevision string `json:"publication_revision"`
	RequestSHA256       string `json:"request_sha256"`
	IndexManifestRef    string `json:"index_manifest_ref"`
	IndexManifestSHA256 string `json:"index_manifest_sha256"`
	ChunkManifestRef    string `json:"chunk_manifest_ref"`
	ChunkManifestSHA256 string `json:"chunk_manifest_sha256"`
	SourceKind          string `json:"source_kind"`
	ContentID           string `json:"content_id"`
	ContentRevisionID   string `json:"content_revision_id"`
	ChunkID             string `json:"chunk_id"`
	ChunkText           string `json:"chunk_text"`
	ChunkTextSHA256     string `json:"chunk_text_sha256"`
	OriginalRef         string `json:"original_ref"`
	OriginalSHA256      string `json:"original_sha256"`
	ContentAvailableAt  string `json:"content_available_at"`
	Grade               *int   `json:"grade"`
	RubricVersion       string `json:"rubric_version"`
	JudgmentSource      string `json:"judgment_source"`
	ActorID             string `json:"actor_id"`
	JudgedAt            string `json:"judged_at"`
	State               string `json:"state"`
	Reason              string `json:"reason"`
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func canonical(raw []byte) ([]byte, error) {
	value, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid canonical event", ErrContract)
	}
	return value, nil
}

func isJudgment(eventType string) bool { return eventType == Revised || eventType == Withdrawn }

func validateBatch(batch eventing.Batch) error {
	if batch.Consumer != DefaultConsumer || batch.Producer != Producer || batch.FromOffset < 1 ||
		len(batch.Events) < 1 || len(batch.Events) > 128 ||
		batch.ToOffset != batch.FromOffset+int64(len(batch.Events))-1 || !shaPattern.MatchString(batch.BatchHash) {
		return ErrContract
	}
	for i, item := range batch.Events {
		if item.Offset != batch.FromOffset+int64(i) || item.Event.Producer != Producer ||
			item.Event.EventID == "" || item.Event.EventType == "" || item.Event.SchemaVersion != 1 ||
			!shaPattern.MatchString(item.InputHash) {
			return ErrContract
		}
	}
	raw, err := json.Marshal(batch.Events)
	if err != nil {
		return err
	}
	encoded, err := canonical(raw)
	if err != nil || digest(encoded) != batch.BatchHash {
		return ErrContract
	}
	return nil
}

func parseJudgment(event eventing.Event, raw []byte, authoritySHA, inputHash string) (Judgment, error) {
	var value Judgment
	if !isJudgment(event.EventType) || !shaPattern.MatchString(authoritySHA) || digest(raw) != authoritySHA {
		return value, ErrContract
	}
	sourceCanonical, err := canonical(raw)
	if err != nil {
		return value, err
	}
	dcRaw, err := json.Marshal(event)
	if err != nil {
		return value, err
	}
	dcCanonical, err := canonical(dcRaw)
	if err != nil || !bytes.Equal(sourceCanonical, dcCanonical) || digest(sourceCanonical) != inputHash {
		return value, ErrContract
	}
	if err := json.Unmarshal(event.Payload, &value); err != nil {
		return value, ErrContract
	}
	if value.JudgmentID == "" || value.RevisionID == "" || value.JudgmentRevision < 1 || value.SearchID == "" ||
		strings.TrimSpace(value.QueryText) == "" || value.ModuleID != event.AggregateID ||
		value.ReleaseID == "" || value.Generation < 1 || value.PublicationRevision == "" ||
		value.IndexManifestRef == "" || value.ChunkManifestRef == "" ||
		value.SourceKind == "" || value.ContentID == "" || value.ContentRevisionID == "" ||
		value.ChunkID == "" || strings.TrimSpace(value.ChunkText) == "" ||
		value.OriginalRef == "" || value.RubricVersion == "" ||
		value.JudgmentSource != "human_judgment" || value.ActorID == "" ||
		strings.TrimSpace(value.Reason) == "" {
		return Judgment{}, ErrContract
	}
	for _, hash := range []string{value.QueryTextSHA256, value.RequestSHA256, value.IndexManifestSHA256,
		value.ChunkManifestSHA256, value.ChunkTextSHA256, value.OriginalSHA256} {
		if !shaPattern.MatchString(hash) {
			return Judgment{}, ErrContract
		}
	}
	if digest([]byte(value.QueryText)) != value.QueryTextSHA256 ||
		digest([]byte(value.ChunkText)) != value.ChunkTextSHA256 {
		return Judgment{}, ErrContract
	}
	contentAt, err := time.Parse(time.RFC3339Nano, value.ContentAvailableAt)
	if err != nil || contentAt.Location() == nil {
		return Judgment{}, ErrContract
	}
	queryAt, err := time.Parse(time.RFC3339Nano, value.QueryTime)
	if err != nil {
		return Judgment{}, ErrContract
	}
	judgedAt, err := time.Parse(time.RFC3339Nano, value.JudgedAt)
	if err != nil || contentAt.After(queryAt) || queryAt.After(judgedAt) {
		return Judgment{}, ErrContract
	}
	if event.EventType == Revised {
		if value.State != "judged" || value.Grade == nil || *value.Grade < 0 || *value.Grade > 3 {
			return Judgment{}, ErrContract
		}
	} else if value.State != "withdrawn" || value.Grade != nil || value.BaseRevisionID == "" {
		return Judgment{}, ErrContract
	}
	return value, nil
}
