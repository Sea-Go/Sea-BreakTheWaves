package catalog

import (
	"context"
	"gorm.io/gorm/clause"
	"time"
)

type Rejected struct {
	ID        string `gorm:"primaryKey"`
	ObjectKey string
	Reason    string
	CreatedAt time.Time
}

func (Rejected) TableName() string { return "btw_rejected_events" }
func (s *Store) Reject(ctx context.Context, id, key, reason string) error {
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&Rejected{ID: id, ObjectKey: key, Reason: reason, CreatedAt: time.Now().UTC()}).Error
}
