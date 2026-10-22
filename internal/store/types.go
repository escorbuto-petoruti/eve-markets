package store

import (
	"context"
	"fmt"
	"strings"
)

// TypeMeta is the cached public metadata of one item type. PackagedVolume is
// zero when the item has none.
type TypeMeta struct {
	TypeID         int64
	Name           string
	GroupID        int64
	GroupName      string
	CategoryID     int64
	CategoryName   string
	Volume         float64
	PackagedVolume float64
}

// UpsertTypeMeta inserts or replaces the cached metadata of one type.
func (s *Store) UpsertTypeMeta(ctx context.Context, m TypeMeta) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO types (type_id, name, group_id, group_name, category_id, category_name, volume, packaged_volume)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(type_id) DO UPDATE SET name = excluded.name, group_id = excluded.group_id,
			group_name = excluded.group_name, category_id = excluded.category_id,
			category_name = excluded.category_name, volume = excluded.volume,
			packaged_volume = excluded.packaged_volume`,
		m.TypeID, m.Name, m.GroupID, m.GroupName, m.CategoryID, m.CategoryName, m.Volume, m.PackagedVolume)
	if err != nil {
		return fmt.Errorf("store: upsert type %d: %w", m.TypeID, err)
	}
	return nil
}

// TypeMetas returns the cached metadata of the given types; unknown ids are
// absent from the map.
func (s *Store) TypeMetas(ctx context.Context, ids []int64) (map[int64]TypeMeta, error) {
	out := make(map[int64]TypeMeta, len(ids))
	const chunk = 500 // stays under SQLite's bound-variable limit
	for len(ids) > 0 {
		batch := ids[:min(chunk, len(ids))]
		ids = ids[len(batch):]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT type_id, name, group_id, group_name, category_id, category_name, volume, packaged_volume
			FROM types WHERE type_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: read types: %w", err)
		}
		for rows.Next() {
			var m TypeMeta
			if err := rows.Scan(&m.TypeID, &m.Name, &m.GroupID, &m.GroupName, &m.CategoryID, &m.CategoryName, &m.Volume, &m.PackagedVolume); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("store: scan type: %w", err)
			}
			out[m.TypeID] = m
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
