package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db     *pgxpool.Pool
	source ContentSource
}

func NewStore(db *pgxpool.Pool, source ContentSource) (*Store, error) {
	if db == nil || source == nil || reflect.ValueOf(source).Kind() == reflect.Pointer && reflect.ValueOf(source).IsNil() {
		return nil, ErrInvalid
	}
	return &Store{db: db, source: source}, nil
}

type Candidate struct {
	Item     ItemFeature `json:"item"`
	Sources  []string    `json:"sources"`
	PoolRank int         `json:"pool_rank"`
}

type CandidatePage struct {
	PoolReleaseID  string      `json:"pool_release_id"`
	Publication    Publication `json:"publication"`
	FeatureVersion string      `json:"feature_version"`
	FeatureHash    string      `json:"feature_hash"`
	BehaviorState  string      `json:"behavior_state"` // not_connected, never a negative label
	Status         string      `json:"status"`         // ready or empty_content
	Candidates     []Candidate `json:"candidates"`
}

func decodeRelease(raw []byte, id string) (PoolRelease, error) {
	var release PoolRelease
	if err := json.Unmarshal(raw, &release); err != nil || release.ID != id || !validRelease(release) {
		return PoolRelease{}, ErrConflict
	}
	canonical, err := json.Marshal(release)
	if err != nil || string(canonical) != string(raw) {
		return PoolRelease{}, ErrConflict
	}
	return release, nil
}

// Rebuild accepts only RTW's twice-checked current publication. A higher RTW
// pointer may supersede an older pool; a stale worker cannot move it back.
func (s *Store) Rebuild(ctx context.Context, moduleID string) (PoolRelease, error) {
	if s == nil || s.db == nil || s.source == nil {
		return PoolRelease{}, ErrInvalid
	}
	release, err := BuildPool(ctx, s.source, moduleID)
	if err != nil {
		return PoolRelease{}, err
	}
	pointer, _ := strconv.ParseInt(release.Publication.PublicationRevision, 10, 64)
	raw, err := json.Marshal(release)
	if err != nil {
		return PoolRelease{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PoolRelease{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, moduleID); err != nil {
		return PoolRelease{}, err
	}
	var oldID string
	var oldPointer int64
	var oldVersion int64
	err = tx.QueryRow(ctx, `SELECT pool_release_id,publication_revision,pointer_version FROM recommend_pool_heads
 WHERE module_id=$1 FOR UPDATE`, moduleID).Scan(&oldID, &oldPointer, &oldVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PoolRelease{}, err
	}
	if oldPointer > pointer || oldPointer == pointer && oldID != "" && oldID != release.ID {
		return PoolRelease{}, ErrStale
	}
	var currentRaw []byte
	err = tx.QueryRow(ctx, `SELECT release_body FROM recommend_pool_releases WHERE pool_release_id=$1`, release.ID).Scan(&currentRaw)
	if err == nil {
		if string(currentRaw) != string(raw) {
			return PoolRelease{}, ErrConflict
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		if _, err = tx.Exec(ctx, `INSERT INTO recommend_pool_releases
 (pool_release_id,module_id,publication_revision,feature_version,feature_hash,release_body)
 VALUES($1,$2,$3,$4,$5,$6)`, release.ID, moduleID, pointer, release.FeatureVersion,
			release.FeatureHash, raw); err != nil {
			return PoolRelease{}, err
		}
	} else {
		return PoolRelease{}, err
	}
	live, err := s.source.Current(ctx, moduleID)
	if err != nil {
		return PoolRelease{}, err
	}
	if !reflect.DeepEqual(live, release.Publication) {
		return PoolRelease{}, ErrStale
	}
	if oldID == "" {
		_, err = tx.Exec(ctx, `INSERT INTO recommend_pool_heads
 (module_id,pool_release_id,publication_revision,pointer_version) VALUES($1,$2,$3,1)`, moduleID, release.ID, pointer)
	} else if oldID != release.ID {
		_, err = tx.Exec(ctx, `UPDATE recommend_pool_heads SET pool_release_id=$2,
 publication_revision=$3,pointer_version=$4,changed_at=clock_timestamp() WHERE module_id=$1`,
			moduleID, release.ID, pointer, oldVersion+1)
	}
	if err != nil {
		return PoolRelease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PoolRelease{}, err
	}
	live, err = s.source.Current(ctx, moduleID)
	if err != nil {
		return PoolRelease{}, err
	}
	if !reflect.DeepEqual(live, release.Publication) {
		return PoolRelease{}, ErrStale
	}
	return release, nil
}

// Candidates serves one immutable pool only while it still equals RTW's
// current publication. Missing RTW or withdrawal fails closed; the old pool
// remains readable only by explicit historical release ID for audit.
func (s *Store) Candidates(ctx context.Context, moduleID string, limit int) (CandidatePage, error) {
	var page CandidatePage
	page.Candidates = []Candidate{}
	if s == nil || s.db == nil || s.source == nil || ctx == nil || !validID(moduleID) ||
		limit < 1 || limit > MaxCandidates {
		return page, ErrInvalid
	}
	live, err := s.source.Current(ctx, moduleID)
	if err != nil {
		return page, err
	}
	if !validPublication(live) || live.ModuleID != moduleID {
		return page, ErrInvalid
	}
	var id string
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT h.pool_release_id,r.release_body FROM recommend_pool_heads h
 JOIN recommend_pool_releases r ON r.pool_release_id=h.pool_release_id WHERE h.module_id=$1`, moduleID).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, ErrUnavailable
	}
	if err != nil {
		return page, err
	}
	release, err := decodeRelease(raw, id)
	if err != nil {
		return page, err
	}
	if !reflect.DeepEqual(live, release.Publication) {
		return page, ErrStale
	}
	items := append([]ItemFeature(nil), release.Items...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ItemID < items[j].ItemID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if limit > len(items) {
		limit = len(items)
	}
	page = CandidatePage{PoolReleaseID: id, Publication: clonePublication(release.Publication),
		FeatureVersion: release.FeatureVersion, FeatureHash: release.FeatureHash,
		BehaviorState: "not_connected", Status: "ready", Candidates: make([]Candidate, 0, limit)}
	if len(items) == 0 {
		page.Status = "empty_content"
	}
	for i := range items[:limit] {
		page.Candidates = append(page.Candidates, Candidate{Item: items[i], Sources: []string{"new_content"}, PoolRank: i + 1})
	}
	again, err := s.source.Current(ctx, moduleID)
	if err != nil {
		return CandidatePage{}, err
	}
	if !reflect.DeepEqual(again, live) {
		return CandidatePage{}, ErrStale
	}
	return page, nil
}

// Release returns an immutable historical input for audit, never a live slate.
func (s *Store) Release(ctx context.Context, id string) (PoolRelease, error) {
	if s == nil || s.db == nil || ctx == nil || len(id) != 64 {
		return PoolRelease{}, ErrInvalid
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT release_body FROM recommend_pool_releases WHERE pool_release_id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolRelease{}, ErrUnavailable
	}
	if err != nil {
		return PoolRelease{}, fmt.Errorf("read pool release: %w", err)
	}
	return decodeRelease(raw, id)
}
