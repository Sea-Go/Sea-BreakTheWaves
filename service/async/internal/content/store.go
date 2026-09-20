package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflict = errors.New("content build conflict")
var ErrInvalidated = errors.New("content revision invalidated")

// Fence is copied from the currently granted DC execution attempt, never
// synthesized by content. A newer DC epoch fences a previous worker locally.
type Fence struct {
	BuildID       string    `json:"-"`
	AttemptID     string    `json:"attempt_id"`
	LeaseEpoch    int64     `json:"lease_epoch"`
	CancelVersion int64     `json:"cancel_version"`
	ExpiresAt     time.Time `json:"lease_expires_at"`
}

type BuildInput struct {
	BuildID     string   `json:"build_id"`
	ModuleID    string   `json:"module_id"`
	ReleaseID   string   `json:"release_id"`
	Generation  int64    `json:"generation"`
	InputHash   string   `json:"input_hash"`
	OperationID string   `json:"operation_id"`
	Revisions   []string `json:"revisions"`
}

type Build struct {
	BuildInput
	Fence
	State     string                `json:"state"`
	Chunks    *corpus.Ref           `json:"chunks,omitempty"`
	Result    *corpus.Ref           `json:"result,omitempty"`
	Lanes     map[string]corpus.Ref `json:"lanes"`
	ErrorCode string                `json:"error_code"`
}

type Store struct{ db *pgxpool.Pool }

// NewStore borrows the pool. Its owner applies the explicit migration and closes
// the pool after all domain services have stopped.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func profileDefinition(id string, size, overlap int, parser, chunker string) string {
	raw, _ := json.Marshal([]any{id, size, overlap, parser, chunker})
	return artifacts.Hash(raw)
}

func (s *Store) bindProfile(ctx context.Context, config ChunkConfig) error {
	hash := profileDefinition(config.ID, config.Size, config.Overlap, "sea.paragraph.v1", "trpc.fixed.v1.8.1")
	_, err := s.db.Exec(ctx, "INSERT INTO content_chunk_profiles(profile_id,definition_hash) VALUES($1,$2) ON CONFLICT DO NOTHING", config.ID, hash)
	if err != nil {
		return err
	}
	var existing string
	if err := s.db.QueryRow(ctx, "SELECT definition_hash FROM content_chunk_profiles WHERE profile_id=$1", config.ID).Scan(&existing); err != nil {
		return err
	}
	if existing != hash {
		return fmt.Errorf("%w: chunk profile changed without a new id", ErrConflict)
	}
	return nil
}

func normalizeInput(input BuildInput) (BuildInput, error) {
	input.Revisions = append([]string(nil), input.Revisions...)
	sort.Strings(input.Revisions)
	if input.BuildID == "" || input.ModuleID == "" || input.ReleaseID == "" || input.Generation <= 0 ||
		input.OperationID == "" || !artifacts.ValidHash(input.InputHash) || len(input.Revisions) == 0 {
		return input, ErrInvalid
	}
	for i, id := range input.Revisions {
		if id == "" || (i > 0 && input.Revisions[i-1] == id) {
			return input, ErrInvalid
		}
	}
	return input, nil
}

func validRef(ref corpus.Ref) bool {
	return artifacts.ValidHash(ref.SHA256) && ref.Key == "sha256/"+ref.SHA256
}

func lockModule(ctx context.Context, tx pgx.Tx, module string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "content/module/"+module)
	return err
}

func checkRevisions(ctx context.Context, tx pgx.Tx, input BuildInput) error {
	var invalid bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM content_tombstones WHERE module_id=$1 AND revision_id=ANY($2))", input.ModuleID, input.Revisions).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return ErrInvalidated
	}
	return nil
}

func (s *Store) Claim(ctx context.Context, input BuildInput, fence Fence) (Build, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return Build{}, err
	}
	if fence.BuildID != input.BuildID || fence.AttemptID == "" || fence.LeaseEpoch <= 0 || fence.CancelVersion < 0 || fence.ExpiresAt.IsZero() {
		return Build{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Build{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockModule(ctx, tx, input.ModuleID); err != nil {
		return Build{}, err
	}
	if err := checkRevisions(ctx, tx, input); err != nil {
		return Build{}, err
	}
	var live bool
	if err := tx.QueryRow(ctx, "SELECT $1::timestamptz > clock_timestamp()", fence.ExpiresAt).Scan(&live); err != nil {
		return Build{}, err
	}
	if !live {
		return Build{}, ErrConflict
	}
	raw, err := json.Marshal(input.Revisions)
	if err != nil {
		return Build{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO content_builds(build_id,module_id,release_id,generation,input_hash,operation_id,revisions,
        attempt_id,lease_epoch,lease_expires_at,cancel_version,state)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'BUILDING') ON CONFLICT(build_id) DO NOTHING`,
		input.BuildID, input.ModuleID, input.ReleaseID, input.Generation, input.InputHash, input.OperationID, raw,
		fence.AttemptID, fence.LeaseEpoch, fence.ExpiresAt, fence.CancelVersion)
	if err != nil {
		return Build{}, err
	}
	current, err := readBuild(ctx, tx, input.BuildID, true)
	if err != nil {
		return Build{}, err
	}
	if !reflect.DeepEqual(current.BuildInput, input) {
		return Build{}, ErrConflict
	}
	if current.State != "BUILDING" {
		return current, tx.Commit(ctx)
	}
	if fence.LeaseEpoch < current.LeaseEpoch || (fence.LeaseEpoch == current.LeaseEpoch && fence.AttemptID != current.AttemptID) || fence.CancelVersion != current.CancelVersion {
		return Build{}, ErrConflict
	}
	// The condition is checked at the write after any lock wait, not at Begin.
	tag, err := tx.Exec(ctx, `UPDATE content_builds SET attempt_id=$2,lease_epoch=$3,lease_expires_at=$4,updated_at=clock_timestamp()
        WHERE build_id=$1 AND $4::timestamptz > clock_timestamp()`, input.BuildID, fence.AttemptID, fence.LeaseEpoch, fence.ExpiresAt)
	if err != nil {
		return Build{}, err
	}
	if tag.RowsAffected() != 1 {
		return Build{}, ErrConflict
	}
	current.Fence = fence
	return current, tx.Commit(ctx)
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readBuild(ctx context.Context, q queryer, id string, lock bool) (Build, error) {
	sql := `SELECT build_id,module_id,release_id,generation,input_hash,operation_id,revisions,
      attempt_id,lease_epoch,lease_expires_at,cancel_version,state,chunks,result,error_code FROM content_builds WHERE build_id=$1`
	if lock {
		sql += " FOR UPDATE"
	}
	var b Build
	var revisions, chunks, result []byte
	err := q.QueryRow(ctx, sql, id).Scan(&b.BuildInput.BuildID, &b.ModuleID, &b.ReleaseID, &b.Generation, &b.InputHash, &b.OperationID, &revisions,
		&b.AttemptID, &b.LeaseEpoch, &b.ExpiresAt, &b.CancelVersion, &b.State, &chunks, &result, &b.ErrorCode)
	if err != nil {
		return b, err
	}
	b.Fence.BuildID = b.BuildInput.BuildID
	if err := json.Unmarshal(revisions, &b.Revisions); err != nil {
		return b, err
	}
	if chunks != nil {
		if err := json.Unmarshal(chunks, &b.Chunks); err != nil {
			return b, err
		}
	}
	if result != nil {
		if err := json.Unmarshal(result, &b.Result); err != nil {
			return b, err
		}
	}
	b.Lanes = map[string]corpus.Ref{}
	rows, err := q.Query(ctx, "SELECT lane,artifact FROM content_build_lanes WHERE build_id=$1 ORDER BY lane", id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var lane string
		var raw []byte
		var ref corpus.Ref
		if err := rows.Scan(&lane, &raw); err != nil {
			return b, err
		}
		if err := json.Unmarshal(raw, &ref); err != nil {
			return b, err
		}
		b.Lanes[lane] = ref
	}
	return b, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Build, error) {
	// Both row and lane metadata come from the same snapshot.
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Build{}, err
	}
	defer tx.Rollback(context.Background())
	b, err := readBuild(ctx, tx, id, false)
	if err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}

func (s *Store) mutate(ctx context.Context, fence Fence, apply func(pgx.Tx, Build) error) error {
	initial, err := s.Get(ctx, fence.BuildID)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockModule(ctx, tx, initial.ModuleID); err != nil {
		return err
	}
	b, err := readBuild(ctx, tx, fence.BuildID, true)
	if err != nil {
		return err
	}
	if b.AttemptID != fence.AttemptID || b.LeaseEpoch != fence.LeaseEpoch || b.CancelVersion != fence.CancelVersion {
		return ErrConflict
	}
	if err := checkRevisions(ctx, tx, b.BuildInput); err != nil {
		return err
	}
	if err := apply(tx, b); err != nil {
		return err
	}
	if b.State == "BUILDING" {
		// Include work after the first guard (lane INSERT or Outbox persistence)
		// in the lease check; a late failure rolls the entire transaction back.
		tag, err := tx.Exec(ctx, `UPDATE content_builds SET updated_at=clock_timestamp()
            WHERE build_id=$1 AND lease_expires_at>clock_timestamp()`, fence.BuildID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	return tx.Commit(ctx)
}

func guardWrite(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, `UPDATE content_builds SET updated_at=clock_timestamp()
      WHERE build_id=$1 AND state='BUILDING' AND lease_expires_at>clock_timestamp()`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) recordChunks(ctx context.Context, fence Fence, ref corpus.Ref) error {
	if !validRef(ref) {
		return ErrInvalid
	}
	return s.mutate(ctx, fence, func(tx pgx.Tx, b Build) error {
		if err := guardWrite(ctx, tx, fence.BuildID); err != nil {
			return err
		}
		if b.Chunks != nil {
			if *b.Chunks == ref {
				return nil
			}
			return ErrConflict
		}
		raw, _ := json.Marshal(ref)
		_, err := tx.Exec(ctx, "UPDATE content_builds SET chunks=$2 WHERE build_id=$1", fence.BuildID, raw)
		return err
	})
}

func (s *Store) RecordLane(ctx context.Context, fence Fence, lane string, ref corpus.Ref) error {
	if (lane != "dense" && lane != "sparse" && lane != "multivector") || !validRef(ref) {
		return ErrInvalid
	}
	return s.mutate(ctx, fence, func(tx pgx.Tx, b Build) error {
		if err := guardWrite(ctx, tx, fence.BuildID); err != nil {
			return err
		}
		if b.Chunks == nil {
			return ErrConflict
		}
		if prior, exists := b.Lanes[lane]; exists {
			if prior == ref {
				return nil
			}
			return ErrConflict
		}
		raw, _ := json.Marshal(ref)
		_, err := tx.Exec(ctx, "INSERT INTO content_build_lanes(build_id,lane,artifact) VALUES($1,$2,$3)", fence.BuildID, lane, raw)
		return err
	})
}

func (s *Store) commitReady(ctx context.Context, fence Fence, result corpus.Ref) error {
	if !validRef(result) {
		return ErrInvalid
	}
	return s.mutate(ctx, fence, func(tx pgx.Tx, b Build) error {
		if b.State == "READY" && b.Result != nil && *b.Result == result {
			return nil
		}
		if b.Chunks == nil || len(b.Lanes) != 3 {
			return ErrConflict
		}
		raw, _ := json.Marshal(result)
		tag, err := tx.Exec(ctx, `UPDATE content_builds SET state='READY',result=$2,updated_at=clock_timestamp()
          WHERE build_id=$1 AND state='BUILDING' AND lease_expires_at>clock_timestamp()`, fence.BuildID, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		payload, err := json.Marshal(struct {
			Input  BuildInput `json:"input"`
			Fence  Fence      `json:"fence"`
			Result corpus.Ref `json:"result"`
		}{b.BuildInput, b.Fence, result})
		if err != nil {
			return err
		}
		eventID := "content.ready:" + artifacts.Hash(payload)
		_, err = tx.Exec(ctx, "INSERT INTO content_outbox(event_id,build_id,payload) VALUES($1,$2,$3)", eventID, fence.BuildID, payload)
		return err
	})
}

// Cancel consumes the authoritative domain cancellation version. It never
// manufactures a new DC attempt or changes a READY historical artifact.
func (s *Store) Cancel(ctx context.Context, id string, version int64) error {
	if version <= 0 {
		return ErrInvalid
	}
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockModule(ctx, tx, b.ModuleID); err != nil {
		return err
	}
	b, err = readBuild(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if version < b.CancelVersion {
		return nil
	}
	if version == b.CancelVersion {
		if b.State == "CANCELLED" {
			return nil
		}
		return ErrConflict
	}
	if b.State != "BUILDING" {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, "UPDATE content_builds SET state='CANCELLED',cancel_version=$2,error_code='CANCELLED',updated_at=clock_timestamp() WHERE build_id=$1", id, version)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Tombstone(ctx context.Context, module, revision string, version int64, inputHash string) error {
	if module == "" || revision == "" || version <= 0 || !artifacts.ValidHash(inputHash) {
		return ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockModule(ctx, tx, module); err != nil {
		return err
	}
	var oldVersion int64
	var oldHash string
	err = tx.QueryRow(ctx, "SELECT aggregate_version,input_hash FROM content_tombstones WHERE module_id=$1 AND revision_id=$2", module, revision).Scan(&oldVersion, &oldHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && version < oldVersion {
		return nil
	}
	if err == nil && version == oldVersion {
		if oldHash == inputHash {
			return nil
		}
		return fmt.Errorf("%w: tombstone version payload differs", ErrConflict)
	}
	_, err = tx.Exec(ctx, `INSERT INTO content_tombstones(module_id,revision_id,aggregate_version,input_hash) VALUES($1,$2,$3,$4)
        ON CONFLICT(module_id,revision_id) DO UPDATE SET aggregate_version=excluded.aggregate_version,input_hash=excluded.input_hash`, module, revision, version, inputHash)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
