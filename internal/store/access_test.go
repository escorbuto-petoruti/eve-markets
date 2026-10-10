package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestStructureAccessRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := s.UpsertUser(ctx, 1, "Alice", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, Token{CharacterID: 1, UserID: 1, CharacterName: "Alice", RefreshToken: "r", UpdatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := s.GetStructureAccess(ctx, 1, 99); err != nil || found {
		t.Fatalf("empty: found=%v err=%v", found, err)
	}
	at := time.Unix(5000, 0)
	if err := s.SaveStructureAccess(ctx, 1, 99, true, at); err != nil {
		t.Fatal(err)
	}
	acc, checked, found, err := s.GetStructureAccess(ctx, 1, 99)
	if err != nil || !found || !acc || !checked.Equal(at) {
		t.Fatalf("got acc=%v checked=%v found=%v err=%v", acc, checked, found, err)
	}
	// Upsert flips the answer and the time.
	later := time.Unix(9000, 0)
	if err := s.SaveStructureAccess(ctx, 1, 99, false, later); err != nil {
		t.Fatal(err)
	}
	acc, checked, found, _ = s.GetStructureAccess(ctx, 1, 99)
	if !found || acc || !checked.Equal(later) {
		t.Fatalf("after upsert: acc=%v checked=%v found=%v", acc, checked, found)
	}
	// Another character does not see it.
	if _, _, found, _ := s.GetStructureAccess(ctx, 2, 99); found {
		t.Error("access leaked across characters")
	}
}

func TestStructureAccessDeletedWithToken(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	_ = s.UpsertUser(ctx, 1, "Alice", time.Unix(1, 0))
	_ = s.SaveToken(ctx, Token{CharacterID: 1, UserID: 1, CharacterName: "Alice", RefreshToken: "r", UpdatedAt: time.Unix(1, 0)})
	if err := s.SaveStructureAccess(ctx, 1, 7, true, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM structure_access`); n != 0 {
		t.Errorf("structure_access rows after token delete = %d, want 0", n)
	}
}

func TestMigration2AppliesOnVersion1WithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "markets.db")
	makeDBAtVersion(t, path, 1)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users VALUES (1, 'Alice', 10);
		INSERT INTO tokens VALUES (1, 1, 'Alice', 'refresh', 'a b', 11);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := readVersion(t, path); v != len(migrations) || v < 2 {
		t.Errorf("user_version = %d, want %d (>=2)", v, len(migrations))
	}
	if n := count(t, s, `SELECT count(*) FROM users`); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
	tok, found, err := s.GetToken(context.Background(), 1)
	if err != nil || !found || tok.RefreshToken != "refresh" {
		t.Errorf("token lost: %+v found=%v err=%v", tok, found, err)
	}
	var marker string
	if err := s.db.QueryRow(`SELECT v FROM marker`).Scan(&marker); err != nil || marker != "before" {
		t.Errorf("marker = %q err=%v", marker, err)
	}
	if err := s.SaveStructureAccess(context.Background(), 1, 5, false, time.Unix(1, 0)); err != nil {
		t.Errorf("new table unusable: %v", err)
	}
}
