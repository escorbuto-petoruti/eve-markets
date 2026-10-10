package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StructureAccess is a cached market-access answer with what ESI said.
// HTTPStatus is 0 and Detail empty when unknown (rows older than migration 4).
type StructureAccess struct {
	Accessible bool
	CheckedAt  time.Time
	HTTPStatus int
	Detail     string
}

// GetStructureAccess returns the cached market-access answer of a character
// for a structure and when it was checked; found is false when none is stored.
func (s *Store) GetStructureAccess(ctx context.Context, characterID, structureID int64) (accessible bool, checkedAt time.Time, found bool, err error) {
	a, found, err := s.GetStructureAccessDetail(ctx, characterID, structureID)
	return a.Accessible, a.CheckedAt, found, err
}

// GetStructureAccessDetail is GetStructureAccess plus the recorded HTTP status
// and error text.
func (s *Store) GetStructureAccessDetail(ctx context.Context, characterID, structureID int64) (StructureAccess, bool, error) {
	var acc, at int64
	var status sql.NullInt64
	var detail sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT accessible, checked_at, http_status, detail FROM structure_access WHERE character_id = ? AND structure_id = ?`,
		characterID, structureID).Scan(&acc, &at, &status, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return StructureAccess{}, false, nil
	}
	if err != nil {
		return StructureAccess{}, false, fmt.Errorf("store: read structure access: %w", err)
	}
	return StructureAccess{Accessible: acc != 0, CheckedAt: time.Unix(at, 0), HTTPStatus: int(status.Int64), Detail: detail.String}, true, nil
}

// SaveStructureAccess stores (or replaces) a definitive market-access answer
// without status or detail. The character must have a stored token.
func (s *Store) SaveStructureAccess(ctx context.Context, characterID, structureID int64, accessible bool, checkedAt time.Time) error {
	return s.SaveStructureAccessDetail(ctx, characterID, structureID, StructureAccess{Accessible: accessible, CheckedAt: checkedAt})
}

// SaveStructureAccessDetail stores (or replaces) a definitive answer with the
// HTTP status and error text ESI gave; a zero status and an empty detail are
// stored as NULL.
func (s *Store) SaveStructureAccessDetail(ctx context.Context, characterID, structureID int64, a StructureAccess) error {
	acc := 0
	if a.Accessible {
		acc = 1
	}
	var status any
	if a.HTTPStatus != 0 {
		status = a.HTTPStatus
	}
	var detail any
	if a.Detail != "" {
		detail = a.Detail
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO structure_access (character_id, structure_id, accessible, checked_at, http_status, detail)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (character_id, structure_id) DO UPDATE SET
			accessible = excluded.accessible,
			checked_at = excluded.checked_at,
			http_status = excluded.http_status,
			detail = excluded.detail`,
		characterID, structureID, acc, a.CheckedAt.Unix(), status, detail)
	if err != nil {
		return fmt.Errorf("store: save structure access: %w", err)
	}
	return nil
}
