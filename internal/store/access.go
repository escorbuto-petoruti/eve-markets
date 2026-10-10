package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetStructureAccess returns the cached market-access answer of a character
// for a structure and when it was checked; found is false when none is stored.
func (s *Store) GetStructureAccess(ctx context.Context, characterID, structureID int64) (accessible bool, checkedAt time.Time, found bool, err error) {
	var acc, at int64
	err = s.db.QueryRowContext(ctx,
		`SELECT accessible, checked_at FROM structure_access WHERE character_id = ? AND structure_id = ?`,
		characterID, structureID).Scan(&acc, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, false, nil
	}
	if err != nil {
		return false, time.Time{}, false, fmt.Errorf("store: read structure access: %w", err)
	}
	return acc != 0, time.Unix(at, 0), true, nil
}

// SaveStructureAccess stores (or replaces) a definitive market-access answer.
// The character must have a stored token.
func (s *Store) SaveStructureAccess(ctx context.Context, characterID, structureID int64, accessible bool, checkedAt time.Time) error {
	acc := 0
	if accessible {
		acc = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO structure_access (character_id, structure_id, accessible, checked_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (character_id, structure_id) DO UPDATE SET
			accessible = excluded.accessible,
			checked_at = excluded.checked_at`,
		characterID, structureID, acc, checkedAt.Unix())
	if err != nil {
		return fmt.Errorf("store: save structure access: %w", err)
	}
	return nil
}
