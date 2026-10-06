package catalog

import (
	"context"
	"gorm.io/gorm/clause"
	"time"
)

// Computation is a retry checkpoint, never a recommended item or source chunk.
type Computation struct {
	ID         string `gorm:"primaryKey"`
	RevisionID string `gorm:"index"`
	Kind       string
	Payload    string
	CreatedAt  time.Time
}

func (Computation) TableName() string { return "btw_computations" }
func (s *Store) Computation(ctx context.Context, id string) (string, error) {
	var v Computation
	e := s.DB.WithContext(ctx).First(&v, "id=?", id).Error
	return v.Payload, NotFound(e)
}
func (s *Store) SaveComputation(ctx context.Context, id, revision, kind, payload string) error {
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&Computation{ID: id, RevisionID: revision, Kind: kind, Payload: payload, CreatedAt: time.Now().UTC()}).Error
}
