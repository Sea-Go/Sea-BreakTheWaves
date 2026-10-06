// Package catalog owns item metadata, durable jobs and serving revisions.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

type Source struct {
	ID         string `gorm:"primaryKey"`
	Producer   string `gorm:"uniqueIndex:btw_source_key,priority:1"`
	ExternalID string `gorm:"uniqueIndex:btw_source_key,priority:2"`
	Kind       string
	Latest     int64
	Serving    string
	Deleted    bool
	UpdatedAt  time.Time
}

func (Source) TableName() string { return "btw_sources" }

type Revision struct {
	OccurredAt  time.Time
	ID          string `gorm:"primaryKey"`
	SourceID    string `gorm:"uniqueIndex:btw_revision_key,priority:1;index"`
	Number      int64  `gorm:"uniqueIndex:btw_revision_key,priority:2"`
	Operation   string
	Fingerprint string
	FileKey     string
	Title       string
	MediaType   string
	SourceURL   string
	Kind        string
	State       string
	Error       string
	TextKey     string
	Digest      string `gorm:"index"`
	Bytes       int64
	Parsed      int
	Understood  int
	CreatedAt   time.Time
}

func (Revision) TableName() string { return "btw_revisions" }

type Event struct {
	Key         string `gorm:"primaryKey"`
	Fingerprint string
	Admission   string
}

func (Event) TableName() string { return "btw_content_events" }

type Record struct {
	TitleKey   string `gorm:"index:btw_semantic_title,priority:3"`
	ID         string `gorm:"primaryKey;index:btw_item_id"`
	RootID     string `gorm:"primaryKey;index:btw_item_root_revision,priority:1;index:btw_semantic_title,priority:1"`
	RevisionID string `gorm:"primaryKey;index:btw_item_root_revision,priority:2;index:btw_semantic_title,priority:2"`
	Kind       string
	RootKind   string
	Payload    string
}

func (Record) TableName() string { return "btw_items" }

type Section struct {
	RevisionID string `gorm:"primaryKey"`
	ID         string `gorm:"primaryKey"`
	Ordinal    int    `gorm:"index:btw_structure_order,priority:2"`
	Payload    string
}

func (Section) TableName() string { return "btw_sections" }

type Relation struct {
	ID         string `gorm:"primaryKey"`
	RevisionID string `gorm:"index"`
	FromID     string `gorm:"index"`
	ToID       string `gorm:"index"`
	Payload    string
}

func (Relation) TableName() string { return "btw_relations" }

type Job struct {
	ID         string `gorm:"primaryKey"`
	RevisionID string `gorm:"index"`
	Kind       string
	Stage      string
	State      string `gorm:"index:btw_job_ready,priority:1"`
	Attempt    int
	Token      string
	LeaseUntil time.Time
	NextRun    time.Time `gorm:"index:btw_job_ready,priority:2"`
	Error      string
	Payload    string
	CreatedAt  time.Time
}

func (Job) TableName() string { return "btw_jobs" }

type Head struct {
	ID       int `gorm:"primaryKey"`
	Sequence int64
}

func (Head) TableName() string { return "btw_serving_head" }

type Change struct {
	Sequence   int64 `gorm:"primaryKey;autoIncrement:false"`
	RootID     string
	RevisionID string
	Deleted    bool
}

func (Change) TableName() string { return "btw_changes" }

type Sample struct {
	ID         string `gorm:"primaryKey"`
	RevisionID string `gorm:"index"`
	Payload    string
}

func (Sample) TableName() string { return "btw_samples" }

type Behavior struct {
	EventID   string `gorm:"primaryKey"`
	RequestID string `gorm:"index"`
	Payload   string
	Exported  bool `gorm:"index"`
	CreatedAt time.Time
}

func (Behavior) TableName() string { return "btw_behavior_outbox" }

type Store struct {
	DB         *gorm.DB
	Partitions int
}

func New(db *gorm.DB) *Store { return &Store{DB: db} }
func (s *Store) Migrate(ctx context.Context) error {
	count := s.Partitions
	if count == 0 {
		count = 32
	}
	if count < 1 || count > 256 {
		return item.ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(current_schema() || '/btw/schema',0))").Error; e != nil {
			return e
		}
		if !tx.Migrator().HasTable(&Record{}) {
			if e := tx.Exec(`CREATE TABLE btw_items (root_id text NOT NULL, id text NOT NULL, revision_id text NOT NULL, kind text, root_kind text, payload text, PRIMARY KEY(root_id,id,revision_id)) PARTITION BY HASH(root_id)`).Error; e != nil {
				return e
			}
			for i := 0; i < count; i++ {
				query := fmt.Sprintf("CREATE TABLE btw_items_p%d PARTITION OF btw_items FOR VALUES WITH (MODULUS %d, REMAINDER %d)", i, count, i)
				if e := tx.Exec(query).Error; e != nil {
					return e
				}
			}
		}
		if e := tx.AutoMigrate(&Source{}, &Revision{}, &Event{}, &Record{}, &Section{}, &Relation{}, &Job{}, &Head{}, &Change{}, &Sample{}, &Behavior{}, &Computation{}, &Navigation{}, &Rejected{}); e != nil {
			return e
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Head{ID: 1}).Error
	})
}
func encode(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return string(b)
}
func sourceID(producer, id string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(producer+"\x00"+id)).String()
}
func NotFound(e error) error {
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return item.ErrNotFound
	}
	return e
}
func (s *Store) Accept(ctx context.Context, e item.ContentEvent, key, fingerprint string, size int64) (out item.Admission, err error) {
	if e.Producer == "" || e.SourceID == "" || e.EventID == "" || e.Revision < 1 || (e.Operation != "upsert" && e.Operation != "delete") {
		return out, item.ErrInvalid
	}
	id := sourceID(e.Producer, e.SourceID)
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Source-local locking serializes arrivals, including first creation.
		if x := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", id).Error; x != nil {
			return x
		}
		var previous Event
		eventKey := e.Producer + ":" + e.EventID
		x := tx.First(&previous, "key = ?", eventKey).Error
		if x == nil {
			if previous.Fingerprint != fingerprint {
				return item.ErrConflict
			}
			if x = json.Unmarshal([]byte(previous.Admission), &out); x != nil {
				return x
			}
			out.Duplicate = true
			return nil
		}
		if !errors.Is(x, gorm.ErrRecordNotFound) {
			return x
		}
		src := Source{ID: id, Producer: e.Producer, ExternalID: e.SourceID, Kind: e.Kind}
		if x = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&src).Error; x != nil {
			return x
		}
		if x = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&src, "id = ?", id).Error; x != nil {
			return x
		}
		if src.Kind == "" && e.Kind != "" {
			src.Kind = e.Kind
		}
		if e.Kind != "" && src.Kind != "" && src.Kind != e.Kind {
			return fmt.Errorf("%w: source kind is immutable", item.ErrConflict)
		}
		var rev Revision
		x = tx.First(&rev, "source_id = ? AND number = ?", id, e.Revision).Error
		if x == nil {
			if rev.Fingerprint != fingerprint {
				return item.ErrConflict
			}
			out = item.Admission{SourceID: id, ItemID: id, RevisionID: rev.ID, Duplicate: true, Obsolete: e.Revision < src.Latest}
			var job Job
			if tx.First(&job, "revision_id = ?", rev.ID).Error == nil {
				out.JobID = job.ID
			}
		} else {
			if !errors.Is(x, gorm.ErrRecordNotFound) {
				return x
			}
			obsolete := e.Revision < src.Latest
			state := "accepted"
			if obsolete {
				state = "obsolete"
			}
			if e.Operation == "delete" {
				state = "deleted"
			}
			rev = Revision{ID: uuid.NewString(), SourceID: id, Number: e.Revision, Operation: e.Operation, Fingerprint: fingerprint, FileKey: key, Title: e.Title, MediaType: e.MediaType, SourceURL: e.SourceURL, Kind: src.Kind, State: state, Bytes: size, OccurredAt: e.OccurredAt, CreatedAt: time.Now().UTC()}
			if x = tx.Create(&rev).Error; x != nil {
				return x
			}
			out = item.Admission{SourceID: id, ItemID: id, RevisionID: rev.ID, Obsolete: obsolete}
			if !obsolete {
				src.Latest = e.Revision
				src.Deleted = e.Operation == "delete"
				if x = tx.Save(&src).Error; x != nil {
					return x
				}
				kind := "process"
				if src.Deleted {
					kind = "delete"
					if x = appendChange(tx, src.ID, rev.ID, true); x != nil {
						return x
					}
				}
				job := Job{ID: uuid.NewString(), RevisionID: rev.ID, Kind: kind, Stage: "accepted", State: "pending", NextRun: time.Now().UTC(), CreatedAt: time.Now().UTC()}
				if x = tx.Create(&job).Error; x != nil {
					return x
				}
				out.JobID = job.ID
			}
		}
		return tx.Create(&Event{Key: eventKey, Fingerprint: fingerprint, Admission: encode(out)}).Error
	})
	return
}
func appendChange(tx *gorm.DB, root, revision string, deleted bool) error {
	var h Head
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&h, 1).Error; e != nil {
		return e
	}
	h.Sequence++
	if e := tx.Save(&h).Error; e != nil {
		return e
	}
	return tx.Create(&Change{Sequence: h.Sequence, RootID: root, RevisionID: revision, Deleted: deleted}).Error
}
func (s *Store) Claim(ctx context.Context, lease time.Duration) (out item.Job, err error) {
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var j Job
		now := time.Now().UTC()
		e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("(state IN ? AND next_run <= ?) OR (state = ? AND lease_until < ?)", []string{"pending", "retry"}, now, "running", now).Order("next_run,id").First(&j).Error
		if e != nil {
			return NotFound(e)
		}
		j.State = "running"
		j.Attempt++
		j.Token = uuid.NewString()
		j.LeaseUntil = now.Add(lease)
		if e = tx.Save(&j).Error; e != nil {
			return e
		}
		out = jobValue(j)
		return nil
	})
	return
}
func jobValue(j Job) item.Job {
	return item.Job{ID: j.ID, RevisionID: j.RevisionID, Kind: j.Kind, Stage: j.Stage, State: j.State, Attempt: j.Attempt, Token: j.Token, LeaseUntil: j.LeaseUntil, NextRun: j.NextRun, Error: j.Error, Payload: json.RawMessage(j.Payload)}
}
func (s *Store) Job(ctx context.Context, id string) (item.Job, error) {
	var j Job
	if e := s.DB.WithContext(ctx).First(&j, "id = ?", id).Error; e != nil {
		return item.Job{}, NotFound(e)
	}
	var revision Revision
	if e := s.DB.WithContext(ctx).Select("parsed", "understood").First(&revision, "id=?", j.RevisionID).Error; e != nil {
		return item.Job{}, NotFound(e)
	}
	out := jobValue(j)
	out.ParsedSections, out.UnderstoodSections = revision.Parsed, revision.Understood
	return out, nil
}
func (s *Store) Renew(ctx context.Context, j item.Job, lease time.Duration) error {
	r := s.DB.WithContext(ctx).Model(&Job{}).Where("id = ? AND token = ? AND state = 'running' AND lease_until > ?", j.ID, j.Token, time.Now().UTC()).Update("lease_until", time.Now().UTC().Add(lease))
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return item.ErrStale
	}
	return nil
}
func (s *Store) Check(ctx context.Context, j item.Job) error {
	var n int64
	e := s.DB.WithContext(ctx).Table("btw_jobs j").Joins("JOIN btw_revisions r ON r.id=j.revision_id").Joins("JOIN btw_sources s ON s.id=r.source_id").Where("j.id=? AND j.token=? AND j.state='running' AND j.lease_until>? AND s.latest=r.number AND s.deleted=?", j.ID, j.Token, time.Now().UTC(), j.Kind == "delete").Count(&n).Error
	if e != nil {
		return e
	}
	if n != 1 {
		return item.ErrStale
	}
	return nil
}
func (s *Store) Fail(ctx context.Context, j item.Job, cause error, permanent bool) error {
	state := "retry"
	if permanent {
		state = "failed"
	}
	if errors.Is(cause, item.ErrStale) {
		state = "obsolete"
	}
	delay := time.Second * time.Duration(1<<min(j.Attempt, 10))
	return s.DB.WithContext(ctx).Model(&Job{}).Where("id=? AND token=? AND state='running'", j.ID, j.Token).Updates(map[string]any{"state": state, "error": cause.Error(), "next_run": time.Now().UTC().Add(delay)}).Error
}
func (s *Store) Stage(ctx context.Context, j item.Job, stage string) error {
	r := s.DB.WithContext(ctx).Model(&Job{}).Where("id=? AND token=? AND state='running' AND lease_until>?", j.ID, j.Token, time.Now().UTC()).Update("stage", stage)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return item.ErrStale
	}
	return nil
}
func (s *Store) Revision(ctx context.Context, id string) (item.Revision, error) {
	var r Revision
	e := s.DB.WithContext(ctx).First(&r, "id = ?", id).Error
	return revisionValue(r), NotFound(e)
}
func revisionValue(r Revision) item.Revision {
	return item.Revision{ID: r.ID, SourceID: r.SourceID, RootID: r.SourceID, Number: r.Number, Kind: r.Kind, Title: r.Title, MediaType: r.MediaType, FileKey: r.FileKey, TextKey: r.TextKey, SourceURL: r.SourceURL, State: r.State, Error: r.Error, Bytes: r.Bytes, ParsedSections: r.Parsed, UnderstoodSections: r.Understood, CreatedAt: r.CreatedAt, OccurredAt: r.OccurredAt}
}
func (s *Store) Sections(ctx context.Context, revision string, after, limit int) ([]item.Section, error) {
	if limit < 1 || limit > 1000 {
		return nil, item.ErrInvalid
	}
	var rows []Section
	if e := s.DB.WithContext(ctx).Where("revision_id=? AND ordinal>?", revision, after).Order("ordinal").Limit(limit).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]item.Section, 0, len(rows))
	for _, r := range rows {
		var v item.Section
		if e := json.Unmarshal([]byte(r.Payload), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) Section(ctx context.Context, revision, id string) (item.Section, error) {
	var r Section
	var out item.Section
	e := s.DB.WithContext(ctx).First(&r, "revision_id=? AND id=?", revision, id).Error
	if e != nil {
		return out, NotFound(e)
	}
	e = json.Unmarshal([]byte(r.Payload), &out)
	return out, e
}
func (s *Store) SaveParsed(ctx context.Context, j item.Job, sections []item.Section, textKey, digest string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := (&Store{DB: tx}).Check(ctx, j); e != nil {
			return e
		}
		for _, section := range sections {
			r := Section{RevisionID: j.RevisionID, ID: section.ID, Ordinal: section.Order, Payload: encode(section)}
			if e := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r).Error; e != nil {
				return e
			}
		}
		return tx.Model(&Revision{}).Where("id=?", j.RevisionID).Updates(map[string]any{"text_key": textKey, "digest": digest, "parsed": len(sections), "state": "parsed"}).Error
	})
}
func (s *Store) SaveUnderstanding(ctx context.Context, j item.Job, items []item.Item, relations []item.Relation, samples []item.Sample, covered int) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := (&Store{DB: tx}).Check(ctx, j); e != nil {
			return e
		}
		for _, v := range items {
			r := Record{TitleKey: strings.ToLower(strings.TrimSpace(v.Title)), ID: v.ID, RootID: v.RootID, RevisionID: v.RevisionID, Kind: v.Kind, RootKind: v.RootKind, Payload: encode(v)}
			if e := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r).Error; e != nil {
				return e
			}
		}
		for _, v := range relations {
			r := Relation{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(j.RevisionID+encode(v))).String(), RevisionID: j.RevisionID, FromID: v.From, ToID: v.To, Payload: encode(v)}
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; e != nil {
				return e
			}
		}
		for _, v := range samples {
			r := Sample{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(j.RevisionID+encode(v))).String(), RevisionID: j.RevisionID, Payload: encode(v)}
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; e != nil {
				return e
			}
		}
		return tx.Model(&Revision{}).Where("id=?", j.RevisionID).Update("understood", covered).Error
	})
}
func (s *Store) Publish(ctx context.Context, j item.Job) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r Revision
		if e := tx.First(&r, "id=?", j.RevisionID).Error; e != nil {
			return e
		}
		var src Source
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&src, "id=?", r.SourceID).Error; e != nil {
			return e
		}
		var job Job
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "id=?", j.ID).Error; e != nil {
			return e
		}
		if job.Token != j.Token || job.State != "running" || job.LeaseUntil.Before(time.Now()) || src.Latest != r.Number || src.Deleted != (j.Kind == "delete") {
			return item.ErrStale
		}
		var head Head
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&head, 1).Error; e != nil {
			return e
		}
		if j.Kind != "delete" {
			if r.Parsed == 0 || r.Understood != r.Parsed {
				return errors.New("incomplete content coverage")
			}
			previous := src.Serving
			if previous != "" && previous != r.ID {
				cleanup := Job{ID: uuid.NewString(), RevisionID: previous, Kind: "prune", Stage: "prune", State: "pending", NextRun: time.Now().UTC(), CreatedAt: time.Now().UTC()}
				if e := tx.Create(&cleanup).Error; e != nil {
					return e
				}
			}
			src.Serving = r.ID
			r.State = "ready"
			if e := tx.Save(&src).Error; e != nil {
				return e
			}
			if e := tx.Save(&r).Error; e != nil {
				return e
			}
			if e := appendChange(tx, src.ID, r.ID, false); e != nil {
				return e
			}
		}
		return tx.Model(&Job{}).Where("id=?", j.ID).Updates(map[string]any{"state": "done", "stage": "indexed", "error": ""}).Error
	})
}
func (s *Store) ItemsForRevision(ctx context.Context, revision, after string, limit int) ([]item.Item, error) {
	var rows []Record
	if limit < 1 || limit > 1000 {
		return nil, item.ErrInvalid
	}
	if e := s.DB.WithContext(ctx).Where("revision_id=? AND id>?", revision, after).Order("id").Limit(limit).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]item.Item, 0, len(rows))
	for _, r := range rows {
		var v item.Item
		if e := json.Unmarshal([]byte(r.Payload), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) Resolve(ctx context.Context, ids, kinds []string) (map[string]item.Item, error) {
	out := map[string]item.Item{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 4096 {
		return nil, item.ErrInvalid
	}
	var rows []Record
	q := s.DB.WithContext(ctx).Table("btw_items i").Select("i.*").Joins("JOIN btw_sources s ON s.id=i.root_id AND s.serving=i.revision_id AND NOT s.deleted").Where("i.id IN ?", ids)
	if len(kinds) > 0 {
		q = q.Where("i.root_kind IN ?", kinds)
	}
	if e := q.Find(&rows).Error; e != nil {
		return nil, e
	}
	for _, r := range rows {
		var v item.Item
		if e := json.Unmarshal([]byte(r.Payload), &v); e != nil {
			return nil, e
		}
		out[r.ID] = v
	}
	return out, nil
}
func (s *Store) Item(ctx context.Context, id string) (item.Item, error) {
	m, e := s.Resolve(ctx, []string{id}, nil)
	if e != nil {
		return item.Item{}, e
	}
	if v, ok := m[id]; ok {
		return v, nil
	}
	var src Source
	e = s.DB.WithContext(ctx).First(&src, "id=? AND NOT deleted", id).Error
	if e != nil {
		return item.Item{}, NotFound(e)
	}
	var r Revision
	e = s.DB.WithContext(ctx).First(&r, "source_id=? AND number=?", id, src.Latest).Error
	if e != nil {
		return item.Item{}, NotFound(e)
	}
	return item.Item{ID: id, RootID: id, Kind: src.Kind, RootKind: src.Kind, RevisionID: r.ID, SourceID: id, Title: r.Title, SourceURL: r.SourceURL}, nil
}
func (s *Store) Readable(ctx context.Context, revision string) error {
	var n int64
	e := s.DB.WithContext(ctx).Table("btw_revisions r").Joins("JOIN btw_sources s ON s.id=r.source_id").Where("r.id=? AND NOT s.deleted AND r.operation='upsert'", revision).Count(&n).Error
	if e != nil {
		return e
	}
	if n != 1 {
		return item.ErrNotFound
	}
	return nil
}
func (s *Store) Digest(ctx context.Context, revision string) (string, error) {
	var r Revision
	e := s.DB.WithContext(ctx).Select("digest").First(&r, "id=?", revision).Error
	return r.Digest, e
}

func (s *Store) CompleteMaintenance(ctx context.Context, j item.Job) error {
	result := s.DB.WithContext(ctx).Model(&Job{}).Where("id=? AND token=? AND state='running' AND lease_until>?", j.ID, j.Token, time.Now().UTC()).Updates(map[string]any{"state": "done", "error": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return item.ErrStale
	}
	return nil
}
