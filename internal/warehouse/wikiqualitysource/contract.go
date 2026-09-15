// Package wikiqualitysource owns a continuous, independent DataCenter cursor
// for RTW Wiki quality judgments. Quality labels are never inferred from
// search judgments, Wiki edits, or unverified DC event payloads.
package wikiqualitysource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const Producer = "ridethewind.knowledge"
const DefaultConsumer = "btw-warehouse-wiki-quality"
const Judged = "knowledge.wiki.quality.judged.v1"

var ErrContract = errors.New("warehouse wiki quality source contract mismatch")
var ErrQualityUnconfigured = errors.New("warehouse wiki quality source authority or rubric verifier unconfigured")
var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func canonical(raw []byte) ([]byte, error) {
	value, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid JSON event", ErrContract)
	}
	return value, nil
}

func qualityEvent(eventType string) bool { return eventType == Judged }

// Unknown quality versions cannot pass as technical skips. Other events of
// this shared producer still occupy real DC offsets in this consumer's ODS.
func qualityFamily(eventType string) bool {
	return strings.HasPrefix(eventType, "knowledge.wiki.quality.")
}

func validateBatch(batch eventing.Batch) error {
	if batch.Consumer != DefaultConsumer || batch.Producer != Producer || batch.FromOffset < 1 ||
		len(batch.Events) < 1 || len(batch.Events) > 128 ||
		batch.ToOffset != batch.FromOffset+int64(len(batch.Events))-1 || !shaPattern.MatchString(batch.BatchHash) {
		return ErrContract
	}
	for i, item := range batch.Events {
		if item.Offset != batch.FromOffset+int64(i) || item.Event.Producer != Producer ||
			item.Event.EventID == "" || item.Event.EventType == "" || item.Event.SchemaVersion != 1 ||
			!shaPattern.MatchString(item.InputHash) ||
			(qualityFamily(item.Event.EventType) && !qualityEvent(item.Event.EventType)) {
			return ErrContract
		}
		raw, err := json.Marshal(item.Event)
		if err != nil {
			return err
		}
		encoded, err := canonical(raw)
		if err != nil || digest(encoded) != item.InputHash {
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

// proveOriginalEvent keeps RTW's original JSON bytes separate from the JCS
// representation that DC commits and hashes. Whitespace/order may differ.
func proveOriginalEvent(event eventing.Event, original []byte, originalSHA, dcInputHash string) error {
	if !qualityEvent(event.EventType) || !shaPattern.MatchString(originalSHA) ||
		digest(original) != originalSHA {
		return ErrContract
	}
	sourceCanonical, err := canonical(original)
	if err != nil {
		return err
	}
	dcRaw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	dcCanonical, err := canonical(dcRaw)
	if err != nil || !bytes.Equal(sourceCanonical, dcCanonical) ||
		digest(sourceCanonical) != dcInputHash {
		return ErrContract
	}
	return nil
}
