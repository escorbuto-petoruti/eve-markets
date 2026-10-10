package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Order event kinds stored in order_events.
const (
	EventNew   = "new"
	EventFill  = "fill"
	EventPrice = "price"
	EventGone  = "gone"
)

// WatchedStructure is one structure a character's market is collected from.
type WatchedStructure struct {
	CharacterID int64
	StructureID int64
	Name        string
	CreatedAt   time.Time
	// LastRunAt is zero until the first collection ran.
	LastRunAt  time.Time
	LastStatus string
	LastError  string
	// OrderCount is the number of stored current orders of the structure; it
	// is only filled by ListWatchedForUser.
	OrderCount int
}

// StoredOrder is an open order of a structure as of the last collection.
// LastSeen is the time the row last changed (unchanged orders are not rewritten).
type StoredOrder struct {
	OrderID      int64
	TypeID       int64
	IsBuy        bool
	Price        float64
	VolumeRemain int64
	VolumeTotal  int64
	Issued       string
	Duration     int
	FirstSeen    time.Time
	LastSeen     time.Time
}

// OrderEvent is one inferred change of an order between two collections.
type OrderEvent struct {
	OrderID      int64
	TypeID       int64
	IsBuy        bool
	At           time.Time
	Kind         string
	Price        float64
	VolumeRemain int64
	DeltaVolume  int64
}

// AddWatch adds (or renames) a watched structure of a character. The
// character must have a stored token, otherwise it wraps ErrNotFound.
func (s *Store) AddWatch(ctx context.Context, characterID, structureID int64, name string, at time.Time) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE character_id = ?`, characterID).Scan(&one)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: watch structure: character %d: %w", characterID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("store: watch structure: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO watched_structures (character_id, structure_id, name, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (character_id, structure_id) DO UPDATE SET name = excluded.name`,
		characterID, structureID, name, at.Unix())
	if err != nil {
		return fmt.Errorf("store: watch structure: %w", err)
	}
	return nil
}

// RemoveWatch stops watching a structure for a character. Collected orders and
// events are kept. Removing a missing watch is not an error.
func (s *Store) RemoveWatch(ctx context.Context, characterID, structureID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM watched_structures WHERE character_id = ? AND structure_id = ?`, characterID, structureID)
	if err != nil {
		return fmt.Errorf("store: unwatch structure: %w", err)
	}
	return nil
}

const watchedColumns = `w.character_id, w.structure_id, COALESCE(w.name, ''), w.created_at,
	w.last_run_at, COALESCE(w.last_status, ''), COALESCE(w.last_error, '')`

func scanWatched(rows *sql.Rows, withCount bool) (WatchedStructure, error) {
	var (
		w       WatchedStructure
		created int64
		lastRun sql.NullInt64
	)
	dest := []any{&w.CharacterID, &w.StructureID, &w.Name, &created, &lastRun, &w.LastStatus, &w.LastError}
	if withCount {
		dest = append(dest, &w.OrderCount)
	}
	if err := rows.Scan(dest...); err != nil {
		return w, fmt.Errorf("store: scan watched structure: %w", err)
	}
	w.CreatedAt = time.Unix(created, 0)
	if lastRun.Valid {
		w.LastRunAt = time.Unix(lastRun.Int64, 0)
	}
	return w, nil
}

func (s *Store) queryWatched(ctx context.Context, withCount bool, query string, args ...any) ([]WatchedStructure, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list watched structures: %w", err)
	}
	defer rows.Close()
	out := []WatchedStructure{}
	for rows.Next() {
		w, err := scanWatched(rows, withCount)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListWatched lists every watched structure, ordered by structure and character.
func (s *Store) ListWatched(ctx context.Context) ([]WatchedStructure, error) {
	return s.queryWatched(ctx, false,
		`SELECT `+watchedColumns+` FROM watched_structures w ORDER BY w.structure_id, w.character_id`)
}

// ListWatchedForUser lists the watched structures of the characters of a user,
// with the number of stored current orders of each structure.
func (s *Store) ListWatchedForUser(ctx context.Context, userID int64) ([]WatchedStructure, error) {
	return s.queryWatched(ctx, true, `
		SELECT `+watchedColumns+`,
			(SELECT count(*) FROM orders_current o WHERE o.structure_id = w.structure_id)
		FROM watched_structures w JOIN tokens t ON t.character_id = w.character_id
		WHERE t.user_id = ?
		ORDER BY w.structure_id, w.character_id`, userID)
}

// RecordRun stores the outcome of a collection. An empty errMsg clears the
// previous error.
func (s *Store) RecordRun(ctx context.Context, characterID, structureID int64, at time.Time, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE watched_structures SET last_run_at = ?, last_status = ?, last_error = ?
		WHERE character_id = ? AND structure_id = ?`,
		at.Unix(), status, errMsg, characterID, structureID)
	if err != nil {
		return fmt.Errorf("store: record collection run: %w", err)
	}
	return nil
}

// CurrentOrders returns the stored open orders of a structure.
func (s *Store) CurrentOrders(ctx context.Context, structureID int64) ([]StoredOrder, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT order_id, type_id, is_buy, price, volume_remain, volume_total, issued, duration, first_seen, last_seen
		FROM orders_current WHERE structure_id = ?`, structureID)
	if err != nil {
		return nil, fmt.Errorf("store: current orders: %w", err)
	}
	defer rows.Close()
	var out []StoredOrder
	for rows.Next() {
		var (
			o      StoredOrder
			buy    int
			fs, ls int64
		)
		if err := rows.Scan(&o.OrderID, &o.TypeID, &buy, &o.Price, &o.VolumeRemain, &o.VolumeTotal, &o.Issued, &o.Duration, &fs, &ls); err != nil {
			return nil, fmt.Errorf("store: scan current order: %w", err)
		}
		o.IsBuy = buy != 0
		o.FirstSeen, o.LastSeen = time.Unix(fs, 0), time.Unix(ls, 0)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ApplyOrderDiff applies one collection result of a structure in a single
// transaction: it upserts the changed orders (first_seen is kept on update),
// deletes the vanished ones and appends the events. Any failure rolls back.
func (s *Store) ApplyOrderDiff(ctx context.Context, structureID int64, at time.Time, upserts []StoredOrder, deletes []int64, events []OrderEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin order diff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	up, err := tx.PrepareContext(ctx, `
		INSERT INTO orders_current (structure_id, order_id, type_id, is_buy, price, volume_remain, volume_total, issued, duration, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (structure_id, order_id) DO UPDATE SET
			price = excluded.price, volume_remain = excluded.volume_remain, volume_total = excluded.volume_total,
			issued = excluded.issued, duration = excluded.duration, last_seen = excluded.last_seen`)
	if err != nil {
		return fmt.Errorf("store: prepare order upsert: %w", err)
	}
	defer up.Close()
	for _, o := range upserts {
		if _, err := up.ExecContext(ctx, structureID, o.OrderID, o.TypeID, b2i(o.IsBuy), o.Price, o.VolumeRemain,
			o.VolumeTotal, o.Issued, o.Duration, o.FirstSeen.Unix(), o.LastSeen.Unix()); err != nil {
			return fmt.Errorf("store: upsert order: %w", err)
		}
	}
	for _, id := range deletes {
		if _, err := tx.ExecContext(ctx, `DELETE FROM orders_current WHERE structure_id = ? AND order_id = ?`, structureID, id); err != nil {
			return fmt.Errorf("store: delete order: %w", err)
		}
	}
	ev, err := tx.PrepareContext(ctx, `
		INSERT INTO order_events (structure_id, order_id, type_id, is_buy, at, kind, price, volume_remain, delta_volume)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: prepare order event: %w", err)
	}
	defer ev.Close()
	for _, e := range events {
		when := e.At
		if when.IsZero() {
			when = at
		}
		if _, err := ev.ExecContext(ctx, structureID, e.OrderID, e.TypeID, b2i(e.IsBuy), when.Unix(), e.Kind,
			e.Price, e.VolumeRemain, e.DeltaVolume); err != nil {
			return fmt.Errorf("store: insert order event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit order diff: %w", err)
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
