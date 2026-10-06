package catalog

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (s *Store) Head(ctx context.Context) (Head, error) {
	var h Head
	e := s.DB.WithContext(ctx).First(&h, 1).Error
	return h, e
}
func (s *Store) VisibleRoots(ctx context.Context, after string, limit int) ([]Source, error) {
	if limit < 1 || limit > 1000 {
		return nil, item.ErrInvalid
	}
	var rows []Source
	e := s.DB.WithContext(ctx).Where("id>? AND serving<>'' AND NOT deleted", after).Order("id").Limit(limit).Find(&rows).Error
	return rows, e
}
func (s *Store) Changes(ctx context.Context, after int64, limit int) ([]Change, error) {
	if limit < 1 || limit > 1000 {
		return nil, item.ErrInvalid
	}
	var rows []Change
	e := s.DB.WithContext(ctx).Where("sequence>?", after).Order("sequence").Limit(limit).Find(&rows).Error
	return rows, e
}
func (s *Store) Samples(ctx context.Context, after string, limit int) ([]Sample, error) {
	if limit < 1 || limit > 1000 {
		return nil, item.ErrInvalid
	}
	var out []Sample
	e := s.DB.WithContext(ctx).Where("id>?", after).Order("id").Limit(limit).Find(&out).Error
	return out, e
}
func (s *Store) RecordBehavior(ctx context.Context, events []item.Behavior) (int, error) {
	if len(events) == 0 || len(events) > 1000 {
		return 0, item.ErrInvalid
	}
	count := 0
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, e := range events {
			if e.EventID == "" || e.RequestID == "" || e.ItemID == "" || e.Scenario == "" || e.OccurredAt.IsZero() {
				return item.ErrInvalid
			}
			switch e.Type {
			case "candidate", "impression", "click", "positive", "negative":
			default:
				return item.ErrInvalid
			}
			r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Behavior{EventID: e.EventID, RequestID: e.RequestID, Payload: encode(e), CreatedAt: time.Now().UTC()})
			if r.Error != nil {
				return r.Error
			}
			count += int(r.RowsAffected)
		}
		return nil
	})
	return count, err
}
func (s *Store) PendingBehavior(ctx context.Context, limit int) ([]Behavior, error) {
	var r []Behavior
	e := s.DB.WithContext(ctx).Where("NOT exported").Order("created_at,event_id").Limit(limit).Find(&r).Error
	return r, e
}
func (s *Store) Exported(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.DB.WithContext(ctx).Model(&Behavior{}).Where("event_id IN ?", ids).Update("exported", true).Error
}
func (s *Store) RevisionError(ctx context.Context, id string, cause error) error {
	return s.DB.WithContext(ctx).Model(&Revision{}).Where("id=?", id).Updates(map[string]any{"state": "failed", "error": cause.Error()}).Error
}
