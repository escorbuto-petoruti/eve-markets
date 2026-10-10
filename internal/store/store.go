// Package store persists users, refresh tokens and login sessions in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

// ErrNotFound reports that a referenced row does not exist.
var ErrNotFound = errors.New("store: not found")

// migrations are applied in order; the number applied is tracked with
// PRAGMA user_version. Never edit an existing entry, append a new one.
var migrations = []string{
	// 1: users (EVE characters that signed in), their stored refresh tokens and
	// login sessions. A token's user_id is separate from its character_id: a
	// user owns their own character and may register more. The user id is the
	// one key that tokens.user_id and sessions.user_id reference; never infer
	// it from a character id, carry it from the session row (store.User.UserID).
	`CREATE TABLE users (
		character_id INTEGER PRIMARY KEY,
		name         TEXT    NOT NULL,
		created_at   INTEGER NOT NULL
	);
	CREATE TABLE tokens (
		character_id   INTEGER PRIMARY KEY,
		user_id        INTEGER NOT NULL REFERENCES users(character_id) ON DELETE CASCADE,
		character_name TEXT    NOT NULL,
		refresh_token  TEXT    NOT NULL,
		scopes         TEXT    NOT NULL,
		updated_at     INTEGER NOT NULL
	);
	CREATE INDEX tokens_user ON tokens (user_id);
	CREATE TABLE sessions (
		id_hash    TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(character_id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX sessions_expires ON sessions (expires_at);`,
	// 2: cached answers of the structure market access probe, per character.
	// Only definitive answers are stored (accessible 1 = yes, 0 = no).
	`CREATE TABLE structure_access (
		character_id INTEGER NOT NULL REFERENCES tokens(character_id) ON DELETE CASCADE,
		structure_id INTEGER NOT NULL,
		accessible   INTEGER NOT NULL,
		checked_at   INTEGER NOT NULL,
		PRIMARY KEY (character_id, structure_id)
	);`,
	// 3: structure demand collection. watched_structures is the watchlist per
	// character; orders_current holds the open orders of a structure as of the
	// last collection; order_events is the append-only log of changes between
	// collections (ESI has no history, demand is inferred from these).
	`CREATE TABLE watched_structures (
		character_id INTEGER NOT NULL REFERENCES tokens(character_id) ON DELETE CASCADE,
		structure_id INTEGER NOT NULL,
		name         TEXT,
		created_at   INTEGER NOT NULL,
		last_run_at  INTEGER,
		last_status  TEXT,
		last_error   TEXT,
		PRIMARY KEY (character_id, structure_id)
	);
	CREATE TABLE orders_current (
		structure_id  INTEGER NOT NULL,
		order_id      INTEGER NOT NULL,
		type_id       INTEGER NOT NULL,
		is_buy        INTEGER NOT NULL,
		price         REAL    NOT NULL,
		volume_remain INTEGER NOT NULL,
		volume_total  INTEGER NOT NULL,
		issued        TEXT    NOT NULL,
		duration      INTEGER NOT NULL,
		first_seen    INTEGER NOT NULL,
		last_seen     INTEGER NOT NULL,
		PRIMARY KEY (structure_id, order_id)
	);
	CREATE INDEX orders_current_type ON orders_current (structure_id, type_id);
	CREATE TABLE order_events (
		id            INTEGER PRIMARY KEY,
		structure_id  INTEGER NOT NULL,
		order_id      INTEGER NOT NULL,
		type_id       INTEGER NOT NULL,
		is_buy        INTEGER NOT NULL,
		at            INTEGER NOT NULL,
		kind          TEXT    NOT NULL CHECK (kind IN ('new', 'fill', 'price', 'gone')),
		price         REAL    NOT NULL,
		volume_remain INTEGER NOT NULL,
		delta_volume  INTEGER NOT NULL
	);
	CREATE INDEX order_events_at ON order_events (structure_id, at);
	CREATE INDEX order_events_type_at ON order_events (structure_id, type_id, at);`,
	// 4: what ESI answered to the market probe (HTTP status and the error
	// text of its body). Rows stored before this migration keep NULL.
	`ALTER TABLE structure_access ADD COLUMN http_status INTEGER;
		ALTER TABLE structure_access ADD COLUMN detail TEXT;`,
}

// Store is a SQLite-backed store.
type Store struct {
	db         *sql.DB
	backupPath string
}

// BackupPath is the backup Open wrote before migrating, or "" when it wrote none.
func (s *Store) BackupPath() string { return s.backupPath }

// Open opens (creating if needed) the database at path and applies pending
// migrations. Use ":memory:" for a private in-memory database.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	} else {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	existed := false
	if path != ":memory:" {
		if _, err := os.Stat(path); err == nil {
			existed = true
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if path == ":memory:" {
		// Each connection would otherwise get its own empty database.
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db}
	if existed {
		bak, err := backupBeforeMigrate(context.Background(), db, path)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		s.backupPath = bak
	}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("store: database schema version %d is newer than supported %d", current, len(migrations))
	}
	for v := current; v < len(migrations); v++ {
		if err := s.applyMigration(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, v int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", v+1, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
		return fmt.Errorf("store: migration %d: %w", v+1, err)
	}
	// PRAGMA does not accept bound parameters.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
		return fmt.Errorf("store: set schema version %d: %w", v+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d: %w", v+1, err)
	}
	return nil
}
