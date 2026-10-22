package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "markets.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenMigrationsIdempotentOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "markets.db")
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s.UpsertUser(ctx, 1, "Alice", time.Unix(100, 0)); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if n := count(t, s2, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("users after reopen = %d, want 1", n)
	}
}

func TestOpenInMemory(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer s.Close()
	if err := s.UpsertUser(context.Background(), 1, "A", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaHasOnlyKnownTables(t *testing.T) {
	s := openTemp(t)
	for _, table := range []string{"users", "tokens", "sessions", "structure_access", "watched_structures", "orders_current", "order_events", "types"} {
		if n := count(t, s, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`); n != 8 {
		t.Errorf("tables = %d, want 8", n)
	}
}
