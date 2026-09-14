package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
	"github.com/jackc/pgx/v5"
)

type CoverageTailEvent struct {
	EventKey EventKey `json:"event_key"`
	Offset   int64    `json:"source_offset"`
	Status   string   `json:"status"`
}

type CoveredState struct {
	Coverage        sourcecoverage.SubjectCoverageRef `json:"coverage"`
	StateVersion    int64                             `json:"state_version"`
	ActiveAtPrefix  []Fact                            `json:"active_at_prefix"`
	Tail            []CoverageTailEvent               `json:"tail_events"`
	CurrentComplete bool                              `json:"current_complete"`
}

// CoveredStateAt reads one repeatable-read PG snapshot. It folds *all*
// accepted sparse history through W, including facts withdrawn by later tail
// events. Latest Current.Active is deliberately not an input to this fold.
func (s *Store) CoveredStateAt(ctx context.Context, subject SubjectRef,
	prefix sourcecoverage.GlobalPrefixRef, cutoff time.Time) (CoveredState, error) {
	if s == nil || s.db == nil || !subject.valid() || cutoff.IsZero() ||
		prefix.SchemaVersion != sourcecoverage.SchemaVersion || prefix.Producer != favoriteCoverageProducer ||
		prefix.BindingPolicyID != FavoriteCoveragePolicyID {
		return CoveredState{}, ErrCoverageUnverified
	}
	w, err := strconv.ParseInt(prefix.ThroughOffset, 10, 64)
	if err != nil || w <= 0 {
		return CoveredState{}, ErrCoverageUnverified
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CoveredState{}, err
	}
	defer tx.Rollback(ctx)
	var prefixBody, subjectBody []byte
	err = tx.QueryRow(ctx, `SELECT ref_body FROM usermodel_coverage_prefix WHERE manifest_sha256=$1`,
		prefix.ManifestSHA256).Scan(&prefixBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return CoveredState{}, ErrCoverageUnverified
	}
	if err != nil {
		return CoveredState{}, err
	}
	var fixedPrefix sourcecoverage.GlobalPrefixRef
	if json.Unmarshal(prefixBody, &fixedPrefix) != nil || !reflect.DeepEqual(fixedPrefix, prefix) {
		return CoveredState{}, ErrCoverageConflict
	}
	err = tx.QueryRow(ctx, `SELECT ref_body FROM usermodel_coverage_subject
		WHERE manifest_sha256=$1 AND authority_id=$2 AND tenant_id=$3 AND subject_id=$4`,
		prefix.ManifestSHA256, subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&subjectBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return CoveredState{}, ErrCoverageUnverified
	}
	if err != nil {
		return CoveredState{}, err
	}
	var ref sourcecoverage.SubjectCoverageRef
	if json.Unmarshal(subjectBody, &ref) != nil || ref.Subject != sourcecoverage.SubjectRef(subject) ||
		!reflect.DeepEqual(ref.Prefix, prefix) {
		return CoveredState{}, ErrCoverageConflict
	}
	out := CoveredState{Coverage: ref, ActiveAtPrefix: []Fact{}, Tail: []CoverageTailEvent{}}
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&out.StateVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CoveredState{}, err
	}
	coveredRows, err := tx.Query(ctx, `SELECT source_offset,event_id,input_hash,normalized_hash,accepted_version
		FROM usermodel_coverage_event WHERE manifest_sha256=$1 AND authority_id=$2 AND tenant_id=$3 AND subject_id=$4
		ORDER BY source_offset`, prefix.ManifestSHA256, subject.AuthorityID, subject.TenantID, subject.SubjectID)
	if err != nil {
		return CoveredState{}, err
	}
	type checkedRow struct {
		offset                             int64
		eventID, inputHash, normalizedHash string
		version                            int64
	}
	proofRows := make([]checkedRow, 0)
	for coveredRows.Next() {
		var row checkedRow
		if err := coveredRows.Scan(&row.offset, &row.eventID, &row.inputHash, &row.normalizedHash, &row.version); err != nil {
			coveredRows.Close()
			return CoveredState{}, err
		}
		proofRows = append(proofRows, row)
	}
	if err := coveredRows.Err(); err != nil {
		coveredRows.Close()
		return CoveredState{}, err
	}
	coveredRows.Close()
	if int64(len(proofRows)) != ref.EventCount {
		return CoveredState{}, ErrCoverageConflict
	}
	factRows, err := tx.Query(ctx, `SELECT e.event_body,e.normalized_hash,e.status,e.accepted_version
		FROM usermodel_events e JOIN usermodel_outbox o ON
			o.authority_id=e.authority_id AND o.tenant_id=e.tenant_id AND o.subject_id=e.subject_id
			AND o.producer=e.producer AND o.event_id=e.event_id AND o.state_version=e.accepted_version
			AND o.event_type='usermodel.fact.accepted'
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3
		AND e.producer=$4 AND e.source_partition=$5 AND e.source_sequence<=$6
		ORDER BY e.source_sequence`, subject.AuthorityID, subject.TenantID, subject.SubjectID,
		prefix.Producer, "dc:"+prefix.Producer, w)
	if err != nil {
		return CoveredState{}, err
	}
	active := map[string]Fact{}
	count := 0
	for factRows.Next() {
		var body []byte
		var fact Fact
		if err := factRows.Scan(&body, &fact.NormalizedHash, &fact.Status, &fact.AcceptedVersion); err != nil {
			factRows.Close()
			return CoveredState{}, err
		}
		if err := json.Unmarshal(body, &fact.Event); err != nil {
			factRows.Close()
			return CoveredState{}, ErrCoverageConflict
		}
		fact.Subject = subject
		_, actualHash, _, hashErr := normalized(fact.Event, true)
		if hashErr != nil || actualHash != fact.NormalizedHash {
			factRows.Close()
			return CoveredState{}, ErrCoverageConflict
		}
		if count >= len(proofRows) {
			factRows.Close()
			return CoveredState{}, ErrCoverageConflict
		}
		row := proofRows[count]
		if fact.SourceSequence != row.offset || fact.EventID != row.eventID ||
			fact.EvidenceHash != row.inputHash || fact.NormalizedHash != row.normalizedHash ||
			fact.Status != "accepted" || fact.AcceptedVersion != row.version {
			factRows.Close()
			return CoveredState{}, ErrCoverageConflict
		}
		if fact.OccurredAt.After(cutoff) || fact.ObservedAt.After(cutoff) {
			factRows.Close()
			return CoveredState{}, ErrCoveragePending
		}
		if fact.Supersedes != nil {
			key := fact.Supersedes.Producer + "\x00" + fact.Supersedes.EventID
			if _, ok := active[key]; !ok {
				factRows.Close()
				return CoveredState{}, ErrCoverageConflict
			}
			delete(active, key)
		}
		if fact.Action != Retract {
			active[fact.Producer+"\x00"+fact.EventID] = fact
		}
		count++
	}
	if err := factRows.Err(); err != nil {
		factRows.Close()
		return CoveredState{}, err
	}
	factRows.Close()
	if count != len(proofRows) {
		return CoveredState{}, ErrCoverageConflict
	}
	for _, fact := range active {
		out.ActiveAtPrefix = append(out.ActiveAtPrefix, fact)
	}
	sort.Slice(out.ActiveAtPrefix, func(i, j int) bool {
		return out.ActiveAtPrefix[i].SourceSequence < out.ActiveAtPrefix[j].SourceSequence
	})
	tailRows, err := tx.Query(ctx, `SELECT producer,event_id,COALESCE(source_sequence,0),status
		FROM usermodel_events WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3
		AND producer=$4 AND source_partition=$5 AND (source_sequence>$6 OR source_sequence IS NULL)
		ORDER BY source_sequence NULLS LAST,event_id`, subject.AuthorityID, subject.TenantID,
		subject.SubjectID, prefix.Producer, "dc:"+prefix.Producer, w)
	if err != nil {
		return CoveredState{}, err
	}
	for tailRows.Next() {
		var event CoverageTailEvent
		if err := tailRows.Scan(&event.EventKey.Producer, &event.EventKey.EventID, &event.Offset, &event.Status); err != nil {
			tailRows.Close()
			return CoveredState{}, err
		}
		out.Tail = append(out.Tail, event)
	}
	if err := tailRows.Err(); err != nil {
		tailRows.Close()
		return CoveredState{}, err
	}
	tailRows.Close()
	out.CurrentComplete = len(out.Tail) == 0 // known PG tail only; DC freshness is a separate H10 gate
	return out, tx.Commit(ctx)
}
