package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/jobs"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// These source-owned values are fixed by the RTW Wiki Compile job provider.
// The technical job hash covers Submit; CompileInputHash covers RTW's business
// page/source/guidance ticket. They must never be treated as one digest.
const WikiCompileJobType = "content.wiki-compile.v1"
const wikiCompileProducer = "ridethewind.knowledge"
const wikiCompileResource = "cpu"
const wikiCompileTicketVersion = "rtw.wiki.compile-ticket.v1"

var ErrWikiCompileJobContract = errors.New("wiki compile job differs from frozen RTW ticket")
var ErrWikiCompileLease = errors.New("wiki compile job lease is no longer current")

var wikiCompileCommandID = regexp.MustCompile(`^command:[0-9a-f]{64}$`)
var wikiCompileSourceEventID = regexp.MustCompile(`^evt_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type wikiCompileTicket struct {
	SchemaVersion        string   `json:"schema_version"`
	SourceEventID        string   `json:"source_event_id"`
	SourceEventJCSDigest string   `json:"source_event_jcs_sha256"`
	CompileID            string   `json:"compile_id"`
	ModuleID             string   `json:"module_id"`
	PageID               string   `json:"page_id"`
	BaseRevisionID       string   `json:"base_revision_id"`
	SourceRevisionIDs    []string `json:"source_revision_ids"`
	GuidanceSHA256       string   `json:"guidance_sha256"`
	CompileInputHash     string   `json:"compile_input_hash"`
	Generation           string   `json:"generation"`
	CancelVersion        string   `json:"cancel_version"`
}

type wikiCompileClaim struct {
	Ticket      wikiCompileTicket
	Generation  int64
	Cancel      int64
	LeaseExpiry time.Time
}

func exactDecimal(value string, allowZero bool) (int64, bool) {
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && (n > 0 || allowZero && n == 0) && strconv.FormatInt(n, 10) == value
}

func decodeWikiTicket(raw json.RawMessage) (wikiCompileTicket, error) {
	var ticket wikiCompileTicket
	if len(raw) < 2 || raw[0] != '{' || len(raw) > 8192 {
		return ticket, ErrWikiCompileJobContract
	}
	// The DC source contract is a small flat object with one string-array field.
	// Reject duplicate, escaped, case-folded and unknown keys before side effects.
	tokens := json.NewDecoder(bytes.NewReader(raw))
	if open, err := tokens.Token(); err != nil || open != json.Delim('{') {
		return ticket, ErrWikiCompileJobContract
	}
	allowed := map[string]bool{
		"schema_version": true, "source_event_id": true,
		"source_event_jcs_sha256": true, "compile_id": true,
		"module_id": true, "page_id": true, "base_revision_id": true,
		"source_revision_ids": true, "guidance_sha256": true,
		"compile_input_hash": true, "generation": true,
		"cancel_version": true,
	}
	seen := make(map[string]bool, len(allowed))
	for tokens.More() {
		start := tokens.InputOffset()
		key, err := tokens.Token()
		end := tokens.InputOffset()
		name, ok := key.(string)
		if err != nil || !ok || !allowed[name] || seen[name] || end > int64(len(raw)) {
			return ticket, ErrWikiCompileJobContract
		}
		literal := bytes.TrimSpace(raw[start:end])
		if len(literal) > 0 && literal[0] == ',' {
			literal = bytes.TrimSpace(literal[1:])
		}
		if !bytes.Equal(literal, []byte(`"`+name+`"`)) {
			return ticket, ErrWikiCompileJobContract
		}
		seen[name] = true
		var value json.RawMessage
		if tokens.Decode(&value) != nil {
			return ticket, ErrWikiCompileJobContract
		}
	}
	if close, err := tokens.Token(); err != nil || close != json.Delim('}') || len(seen) != len(allowed) {
		return ticket, ErrWikiCompileJobContract
	}
	if _, err := tokens.Token(); err != io.EOF {
		return ticket, ErrWikiCompileJobContract
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&ticket) != nil || d.Decode(new(any)) != io.EOF {
		return wikiCompileTicket{}, ErrWikiCompileJobContract
	}
	return ticket, nil
}

// DecodeWikiCompileClaim is the deterministic first-effect gate. It verifies
// the whole DC Submit JCS/hash, the fixed source ticket and current lease;
// RTW GetCompile supplies a separate authoritative business comparison later.
func DecodeWikiCompileClaim(job jobs.Job, workerID string, now time.Time) (wikiCompileClaim, error) {
	var out wikiCompileClaim
	if workerID == "" || job.ID == "" || job.State != "running" || job.WorkerID != workerID ||
		job.Request.JobType != WikiCompileJobType || job.Request.ResourceProfile != wikiCompileResource ||
		job.Request.Producer != wikiCompileProducer || !wikiCompileCommandID.MatchString(job.Request.OperationID) ||
		job.Request.MaxAttempts != 3 || job.Attempt < 1 || job.AttemptID == "" ||
		job.LeaseEpoch < 1 || job.CancelVersion < 0 ||
		!artifacts.ValidHash(job.InputHash) {
		return out, ErrWikiCompileJobContract
	}
	deadline, deadlineErr := time.Parse(time.RFC3339Nano, job.Request.Deadline)
	expiry, expiryErr := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if deadlineErr != nil || expiryErr != nil || !deadline.After(now) || !expiry.After(now) || expiry.After(deadline) {
		return out, ErrWikiCompileLease
	}
	raw, err := json.Marshal(job.Request)
	if err != nil {
		return out, ErrWikiCompileJobContract
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil || artifacts.Hash(canonical) != job.InputHash {
		return out, ErrWikiCompileJobContract
	}
	ticket, err := decodeWikiTicket(job.Request.Input)
	if err != nil || ticket.SchemaVersion != wikiCompileTicketVersion ||
		!wikiCompileSourceEventID.MatchString(ticket.SourceEventID) ||
		!artifacts.ValidHash(ticket.SourceEventJCSDigest) || !artifacts.ValidHash(ticket.GuidanceSHA256) ||
		!artifacts.ValidHash(ticket.CompileInputHash) || ticket.CompileID == "" || ticket.ModuleID == "" ||
		ticket.PageID == "" || len(ticket.SourceRevisionIDs) < 1 || len(ticket.SourceRevisionIDs) > 16 ||
		job.Request.RunRef != "wiki-compile/"+ticket.CompileID {
		return out, ErrWikiCompileJobContract
	}
	for i, id := range ticket.SourceRevisionIDs {
		if id == "" || i > 0 && ticket.SourceRevisionIDs[i-1] >= id {
			return out, ErrWikiCompileJobContract
		}
	}
	generation, ok := exactDecimal(ticket.Generation, false)
	if !ok {
		return out, ErrWikiCompileJobContract
	}
	cancel, ok := exactDecimal(ticket.CancelVersion, true)
	if !ok || ticket.CancelVersion != "0" || cancel != job.CancelVersion {
		return out, ErrWikiCompileJobContract
	}
	out = wikiCompileClaim{Ticket: ticket, Generation: generation, Cancel: cancel, LeaseExpiry: expiry}
	return out, nil
}

func wikiCompileGuidanceHash(guidance string) string {
	sum := sha256.Sum256([]byte(guidance))
	return hex.EncodeToString(sum[:])
}
