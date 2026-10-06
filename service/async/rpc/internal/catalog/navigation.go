package catalog

import (
	"context"
	"encoding/json"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"gorm.io/gorm/clause"
	"strings"
)

type Navigation struct {
	RevisionID string `gorm:"primaryKey"`
	Ordinal    int    `gorm:"primaryKey;autoIncrement:false"`
	Payload    string
}

func (Navigation) TableName() string { return "btw_navigation" }
func (s *Store) SaveNavigation(ctx context.Context, revision string, entries []item.Navigation) error {
	for i, v := range entries {
		row := Navigation{RevisionID: revision, Ordinal: i + 1, Payload: encode(v)}
		if e := s.DB.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) Navigation(ctx context.Context, revision string, after, limit int) ([]item.Navigation, error) {
	if limit < 1 || limit > 1000 {
		return nil, item.ErrInvalid
	}
	var rows []Navigation
	if e := s.DB.WithContext(ctx).Where("revision_id=? AND ordinal>?", revision, after).Order("ordinal").Limit(limit).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := []item.Navigation{}
	for _, v := range rows {
		var entry item.Navigation
		if e := json.Unmarshal([]byte(v.Payload), &entry); e != nil {
			return nil, e
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *Store) Relations(ctx context.Context, id, revision string, limit int) ([]item.Relation, error) {
	var rows []Relation
	if e := s.DB.WithContext(ctx).Where("revision_id=? AND (from_id=? OR to_id=?)", revision, id, id).Order("id").Limit(limit).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := []item.Relation{}
	for _, r := range rows {
		var v item.Relation
		if e := json.Unmarshal([]byte(r.Payload), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) RelationsForRevision(ctx context.Context, revision, after string, limit int) ([]item.Relation, string, error) {
	if limit < 1 || limit > 1000 {
		return nil, "", item.ErrInvalid
	}
	var rows []Relation
	if err := s.DB.WithContext(ctx).Where("revision_id=? AND id>?", revision, after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	out := make([]item.Relation, 0, len(rows))
	for _, row := range rows {
		var relation item.Relation
		if err := json.Unmarshal([]byte(row.Payload), &relation); err != nil {
			return nil, "", err
		}
		out = append(out, relation)
	}
	if len(rows) > 0 {
		after = rows[len(rows)-1].ID
	}
	return out, after, nil
}
func (s *Store) ByTitle(ctx context.Context, root, revision, title string) (string, error) {
	var row Record
	e := s.DB.WithContext(ctx).Select("id").Where("root_id=? AND revision_id=? AND title_key=?", root, revision, strings.ToLower(strings.TrimSpace(title))).Order("id").First(&row).Error
	return row.ID, NotFound(e)
}
